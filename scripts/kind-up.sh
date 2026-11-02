#!/usr/bin/env bash
# Поднимает кластер kind и разворачивает в нём Мемопедию.
# Запуск из корня репозитория: ./scripts/kind-up.sh
# Повторный запуск безопасен: пересоберёт образ и обновит приложение.
set -euo pipefail

CLUSTER=memes
IMAGE=meme-o-pedia:dev
METRICS_SERVER_VERSION=v0.7.2

if [[ ! -f .env ]]; then
  echo "Нет файла .env. Создайте его: cp .env.example .env и впишите свои пароли." >&2
  exit 1
fi
# shellcheck disable=SC1091
source .env
: "${DB_PASSWORD:?DB_PASSWORD не задан в .env}"
: "${ADMIN_TOKEN:?ADMIN_TOKEN не задан в .env}"

if ! kind get clusters | grep -qx "$CLUSTER"; then
  echo "==> Создаю кластер $CLUSTER"
  kind create cluster --config deploy/kind-config.yaml

  echo "==> Ставлю metrics-server (нужен для HPA)"
  kubectl apply -f "https://github.com/kubernetes-sigs/metrics-server/releases/download/${METRICS_SERVER_VERSION}/components.yaml"
  # В kind у kubelet самоподписанные сертификаты — разрешаем их metrics-server.
  kubectl -n kube-system patch deployment metrics-server --type=json \
    -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
fi

echo "==> Собираю образ $IMAGE и загружаю его в узлы кластера"
docker build -t "$IMAGE" .
kind load docker-image "$IMAGE" --name "$CLUSTER"

echo "==> Создаю Secret с паролями из .env"
kubectl apply -f deploy/k8s/namespace.yaml
kubectl -n memes create secret generic meme-secrets \
  --from-literal=db-password="$DB_PASSWORD" \
  --from-literal=admin-token="$ADMIN_TOKEN" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "==> Применяю манифесты"
kubectl apply -k deploy/k8s
# Образ с тем же тегом мог измениться — перезапускаем поды, чтобы подхватили новый.
kubectl -n memes rollout restart deployment/api
kubectl -n memes rollout status statefulset/postgres --timeout=180s
kubectl -n memes rollout status deployment/api --timeout=180s

echo
echo "Готово: http://localhost:8080"
kubectl -n memes get pods -o wide
