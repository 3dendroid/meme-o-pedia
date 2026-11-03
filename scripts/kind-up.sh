#!/usr/bin/env bash
# Creates a kind cluster and deploys Meme-o-pedia into it.
# Run from the repository root: ./scripts/kind-up.sh
# Safe to run again: it rebuilds the image and updates the app.
set -euo pipefail

CLUSTER=memes
IMAGE=meme-o-pedia:dev
METRICS_SERVER_VERSION=v0.7.2
ENVOY_GATEWAY_VERSION=v1.9.2

if [[ ! -f .env ]]; then
  echo "No .env file. Create it with: cp .env.example .env and set your passwords." >&2
  exit 1
fi
# shellcheck disable=SC1091
source .env
: "${DB_PASSWORD:?DB_PASSWORD is not set in .env}"
: "${ADMIN_TOKEN:?ADMIN_TOKEN is not set in .env}"
: "${S3_ACCESS_KEY:?S3_ACCESS_KEY is not set in .env (see .env.example)}"
: "${S3_SECRET_KEY:?S3_SECRET_KEY is not set in .env (see .env.example)}"
: "${GRAFANA_ADMIN_PASSWORD:?GRAFANA_ADMIN_PASSWORD is not set in .env (see .env.example)}"
# The keys go into a JSON file as is, so keep them simple.
if [[ ! "$S3_ACCESS_KEY$S3_SECRET_KEY" =~ ^[A-Za-z0-9]+$ ]]; then
  echo "S3_ACCESS_KEY and S3_SECRET_KEY may contain only letters and digits." >&2
  exit 1
fi

if ! kind get clusters | grep -qx "$CLUSTER"; then
  echo "==> Creating cluster $CLUSTER"
  kind create cluster --config deploy/kind-config.yaml

  echo "==> Installing metrics-server (required by the HPA)"
  kubectl apply -f "https://github.com/kubernetes-sigs/metrics-server/releases/download/${METRICS_SERVER_VERSION}/components.yaml"
  # kubelets in kind use self-signed certificates; let metrics-server accept them.
  kubectl -n kube-system patch deployment metrics-server --type=json \
    -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
fi

echo "==> Installing Envoy Gateway ${ENVOY_GATEWAY_VERSION} (runs the Gateway API objects)"
# --server-side: the file holds large CRDs that do not fit into a client-side apply annotation.
kubectl apply --server-side --force-conflicts \
  -f "https://github.com/envoyproxy/gateway/releases/download/${ENVOY_GATEWAY_VERSION}/install.yaml"
kubectl -n envoy-gateway-system wait --for=condition=Available deployment/envoy-gateway --timeout=300s
kubectl apply -f deploy/gateway-class.yaml

echo "==> Building image $IMAGE and loading it into the cluster nodes"
docker build -t "$IMAGE" .
kind load docker-image "$IMAGE" --name "$CLUSTER"

echo "==> Creating the Secret with passwords from .env"
kubectl apply -f deploy/k8s/namespace.yaml
kubectl -n memes create secret generic meme-secrets \
  --from-literal=db-password="$DB_PASSWORD" \
  --from-literal=admin-token="$ADMIN_TOKEN" \
  --from-literal=s3-access-key="$S3_ACCESS_KEY" \
  --from-literal=s3-secret-key="$S3_SECRET_KEY" \
  --from-literal=grafana-admin-password="$GRAFANA_ADMIN_PASSWORD" \
  --dry-run=client -o yaml | kubectl apply -f -

# SeaweedFS reads its keys from a JSON file; this Secret becomes that file in the pod.
S3_JSON=$(printf '{"identities":[{"name":"memes","credentials":[{"accessKey":"%s","secretKey":"%s"}],"actions":["Admin","Read","Write","List","Tagging"]}]}' \
  "$S3_ACCESS_KEY" "$S3_SECRET_KEY")
kubectl -n memes create secret generic seaweedfs-s3 \
  --from-literal=s3.json="$S3_JSON" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "==> Applying manifests"
kubectl apply -k deploy/k8s
# The image may have changed under the same tag: restart the pods so they pick it up.
kubectl -n memes rollout restart deployment/api
kubectl -n memes rollout status statefulset/postgres --timeout=180s
kubectl -n memes rollout status statefulset/seaweedfs --timeout=300s
kubectl -n memes rollout status deployment/api --timeout=180s
kubectl -n memes wait --for=condition=Programmed gateway/memes --timeout=300s
kubectl -n memes rollout status deployment/prometheus --timeout=300s
kubectl -n memes rollout status deployment/grafana --timeout=300s

echo
echo "Done: http://localhost:8080 (Grafana: http://localhost:8080/grafana/, user admin)"
kubectl -n memes get pods -o wide
