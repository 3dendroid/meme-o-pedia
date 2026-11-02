#!/usr/bin/env bash
# Creates a kind cluster and deploys Meme-o-pedia into it.
# Run from the repository root: ./scripts/kind-up.sh
# Safe to run again: it rebuilds the image and updates the app.
set -euo pipefail

CLUSTER=memes
IMAGE=meme-o-pedia:dev
METRICS_SERVER_VERSION=v0.7.2

if [[ ! -f .env ]]; then
  echo "No .env file. Create it with: cp .env.example .env and set your passwords." >&2
  exit 1
fi
# shellcheck disable=SC1091
source .env
: "${DB_PASSWORD:?DB_PASSWORD is not set in .env}"
: "${ADMIN_TOKEN:?ADMIN_TOKEN is not set in .env}"

if ! kind get clusters | grep -qx "$CLUSTER"; then
  echo "==> Creating cluster $CLUSTER"
  kind create cluster --config deploy/kind-config.yaml

  echo "==> Installing metrics-server (required by the HPA)"
  kubectl apply -f "https://github.com/kubernetes-sigs/metrics-server/releases/download/${METRICS_SERVER_VERSION}/components.yaml"
  # kubelets in kind use self-signed certificates; let metrics-server accept them.
  kubectl -n kube-system patch deployment metrics-server --type=json \
    -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
fi

echo "==> Building image $IMAGE and loading it into the cluster nodes"
docker build -t "$IMAGE" .
kind load docker-image "$IMAGE" --name "$CLUSTER"

echo "==> Creating the Secret with passwords from .env"
kubectl apply -f deploy/k8s/namespace.yaml
kubectl -n memes create secret generic meme-secrets \
  --from-literal=db-password="$DB_PASSWORD" \
  --from-literal=admin-token="$ADMIN_TOKEN" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "==> Applying manifests"
kubectl apply -k deploy/k8s
# The image may have changed under the same tag: restart the pods so they pick it up.
kubectl -n memes rollout restart deployment/api
kubectl -n memes rollout status statefulset/postgres --timeout=180s
kubectl -n memes rollout status deployment/api --timeout=180s

echo
echo "Done: http://localhost:8080"
kubectl -n memes get pods -o wide
