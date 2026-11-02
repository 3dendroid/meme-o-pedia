# meme-o-pedia

Энциклопедия мемов: учебный проект для Kubernetes.

Сайт показывает мемы с описанием, страной происхождения, годом появления и актуальностью;
их можно фильтровать по стране, сортировать по дате и искать.
Само приложение намеренно простое — основная цель проекта научиться запускать его в Kubernetes:
база данных, секреты, кэш, пробы и автомасштабирование под нагрузкой.

## Из чего состоит

| Часть | Что это | В Kubernetes |
|---|---|---|
| API + веб-страница | Go, один бинарник, образ ~20 МБ | Deployment + Service (NodePort) + HPA |
| PostgreSQL 16 | хранит мемы | StatefulSet + PersistentVolumeClaim |
| Redis 7 | кэширует ответы API на 60 секунд | Deployment |
| Пароли | пароль базы и токен администратора | Secret `meme-secrets` (создаётся из `.env`, в git не попадает) |

```
.
├── main.go, handlers.go, store.go, cache.go   # код API
├── handlers_test.go                           # тесты
├── migrations/                                # схема базы и стартовые мемы (вшиты в бинарник)
├── web/index.html                             # страница сайта
├── Dockerfile                                 # двухэтапная сборка образа
├── docker-compose.yml                         # запуск без Kubernetes
├── deploy/kind-config.yaml                    # кластер kind из 3 узлов
├── deploy/k8s/                                # манифесты Kubernetes
├── scripts/kind-up.sh                         # поднять кластер и развернуть всё одной командой
└── loadtest/k6.js                             # нагрузочный тест
```

## API

| Запрос | Что делает |
|---|---|
| `GET /api/memes?country=RU&q=кот&sort=old` | список мемов; все параметры необязательные, `sort=old` — сначала старые |
| `GET /api/memes/{id}` | один мем |
| `GET /api/countries` | страны и количество мемов в каждой |
| `POST /api/memes` | добавить мем; нужен заголовок `Authorization: Bearer <ADMIN_TOKEN>` |
| `GET /healthz` | liveness-проба: процесс жив |
| `GET /readyz` | readiness-проба: база отвечает |

Заголовок ответа `X-Cache: HIT` или `MISS` показывает, пришёл ли ответ из Redis.

Пример добавления мема:

```bash
curl -X POST http://localhost:8080/api/memes \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"title":"Новый мем","description":"Описание","country_code":"RU","country_name":"Россия","year":2026,"status":"trend"}'
```

`status` — одно из `trend` (в тренде), `classic` (классика), `outdated` (устарел).

## Подготовка Ubuntu

Docker:

```bash
curl -fsSL https://get.docker.com | sudo sh
```

```bash
sudo usermod -aG docker $USER
```

После этой команды выйдите из системы и войдите снова.

kubectl и kind:

```bash
curl -LO "https://dl.k8s.io/release/$(curl -Ls https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" && sudo install kubectl /usr/local/bin/ && rm kubectl
```

```bash
curl -Lo kind https://github.com/kubernetes-sigs/kind/releases/download/v0.29.0/kind-linux-amd64 && sudo install kind /usr/local/bin/ && rm kind
```

Виртуалке нужно минимум 2 ядра и 4 ГБ памяти (лучше 8 ГБ).

## Шаг 1. Пароли

```bash
cp .env.example .env
```

Откройте `.env` и впишите свои значения. Этот файл в git не попадает.

## Шаг 2. Запуск без Kubernetes (проверить, что всё работает)

```bash
docker compose up --build
```

Сайт: http://localhost:8080. Остановить и удалить данные:

```bash
docker compose down -v
```

## Шаг 3. Запуск в Kubernetes (kind)

```bash
./scripts/kind-up.sh
```

Скрипт создаёт кластер из трёх узлов, ставит metrics-server (без него не работает HPA),
собирает образ, загружает его в кластер, создаёт Secret из `.env` и применяет манифесты.
Повторный запуск пересобирает образ и обновляет приложение.

Сайт: http://localhost:8080.

Если виртуалка без графики, откройте сайт с вашего компьютера через SSH-туннель
и затем зайдите в браузере на http://localhost:8080:

```bash
ssh -L 8080:localhost:8080 user@адрес-виртуалки
```

Полезные команды:

```bash
kubectl -n memes get pods -o wide
```

```bash
kubectl -n memes logs deploy/api -f
```

```bash
kubectl -n memes get hpa
```

Удалить кластер целиком:

```bash
kind delete cluster --name memes
```

## Шаг 4. Нагрузка и автомасштабирование

В одном терминале следите за HPA и подами (экран обновляется каждые 2 секунды):

```bash
watch -n 2 kubectl -n memes get hpa,pods
```

В другом запустите нагрузочный тест (k6 ставить не нужно, он запускается в Docker):

```bash
docker run --rm -i --network host grafana/k6 run - < loadtest/k6.js
```

Через минуту-две загрузка CPU превысит 60%, и HPA начнёт добавлять поды API (до 8).
После окончания теста лишние поды удалятся примерно через 5 минут.

## Что попробовать дальше

- Удалите под API (`kubectl -n memes delete pod <имя>`) и посмотрите, как Deployment создаёт новый.
- Удалите под Postgres: данные останутся, потому что они лежат на PersistentVolume.
- Остановите Redis (`kubectl -n memes scale deploy/redis --replicas=0`): сайт продолжит работать, но медленнее.
- Сравните результаты k6 с Redis и без него.
- Поменяйте пароль в `.env`, перезапустите скрипт и разберитесь, почему Postgres его не принял
  (подсказка: пароль задаётся только при первом создании базы).

Следующие шаги проекта: картинки мемов в MinIO, CronJob для пересчёта актуальности,
доступ через Ingress или Gateway API, метрики в Prometheus и Grafana, хранение секретов вне кластера.
