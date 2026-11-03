# meme-o-pedia

A meme encyclopedia: a learning project for Kubernetes.

The site lists memes with a description, country of origin, year and relevance;
you can filter them by country, sort by date and search.
The app is deliberately simple. The real goal is to learn how to run it in Kubernetes:
a database, secrets, a cache, probes and autoscaling under load.

## What's inside

| Part | What it is | In Kubernetes |
|---|---|---|
| API + web page | Go, a single binary, ~20 MB image | Deployment + Service (NodePort) + HPA |
| PostgreSQL 16 | stores the memes | StatefulSet + PersistentVolumeClaim |
| Redis 7 | caches API responses for 60 seconds | Deployment |
| SeaweedFS | S3-compatible storage for meme pictures | StatefulSet + PersistentVolumeClaim |
| Relevance job | once a day turns old "trend" memes into "classic" | CronJob |
| Passwords | database password, admin token, storage keys | Secrets `meme-secrets` and `seaweedfs-s3` (created from `.env`, never committed) |

```
.
├── main.go, handlers.go, store.go, cache.go   # API code
├── recalc.go                                  # the daily relevance job
├── images.go                                  # picture storage over S3
├── handlers_test.go                           # tests
├── migrations/                                # database schema and starter memes (embedded in the binary)
├── web/index.html                             # the web page
├── Dockerfile                                 # two-stage image build
├── docker-compose.yml                         # run without Kubernetes
├── deploy/kind-config.yaml                    # three-node kind cluster
├── deploy/k8s/                                # Kubernetes manifests
├── scripts/kind-up.sh                         # create the cluster and deploy everything with one command
└── loadtest/k6.js                             # load test
```

## API

| Request | What it does |
|---|---|
| `GET /api/memes?country=US&q=cat&sort=old` | list memes; all parameters are optional, `sort=old` means oldest first |
| `GET /api/memes/{id}` | a single meme |
| `GET /api/countries` | countries and the number of memes in each |
| `POST /api/memes` | add a meme; requires the `Authorization: Bearer <ADMIN_TOKEN>` header |
| `PUT /api/memes/{id}/image` | upload a picture (PNG, JPEG, GIF or WebP, up to 5 MB); requires the admin token |
| `GET /images/{key}` | a picture; the link comes from the meme's `image_url` |
| `GET /healthz` | liveness probe: the process is alive |
| `GET /readyz` | readiness probe: the database answers |

The `X-Cache: HIT` or `MISS` response header shows whether the response came from Redis.

Adding a meme:

```bash
curl -X POST http://localhost:8080/api/memes \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"title":"New meme","description":"Description","country_code":"US","country_name":"United States","year":2026,"status":"trend"}'
```

`status` is one of `trend`, `classic` or `outdated`.

## Preparing Ubuntu

Docker:

```bash
curl -fsSL https://get.docker.com | sudo sh
```

```bash
sudo usermod -aG docker $USER
```

Log out and back in after this command.

kubectl and kind:

```bash
curl -LO "https://dl.k8s.io/release/$(curl -Ls https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" && sudo install kubectl /usr/local/bin/ && rm kubectl
```

```bash
curl -Lo kind https://github.com/kubernetes-sigs/kind/releases/download/v0.29.0/kind-linux-amd64 && sudo install kind /usr/local/bin/ && rm kind
```

The machine needs at least 2 CPU cores and 4 GB of RAM (8 GB is better).

## Step 1. Passwords

```bash
cp .env.example .env
```

Open `.env` and set your own values. This file never goes to git.

## Step 2. Run without Kubernetes (check that everything works)

```bash
docker compose up --build
```

Site: http://localhost:8080. To stop and delete the data:

```bash
docker compose down -v
```

## Step 3. Run in Kubernetes (kind)

```bash
./scripts/kind-up.sh
```

The script creates a three-node cluster, installs metrics-server (the HPA needs it),
builds the image, loads it into the cluster, creates the Secret from `.env` and applies the manifests.
Running it again rebuilds the image and updates the app.

Site: http://localhost:8080, or `http://<machine IP>:8080` from another computer on the same network.

If the machine has no desktop, open the site from your computer through an SSH tunnel
and then go to http://localhost:8080 in your browser:

```bash
ssh -L 8080:localhost:8080 user@machine-address
```

Useful commands:

```bash
kubectl -n memes get pods -o wide
```

```bash
kubectl -n memes logs deploy/api -f
```

```bash
kubectl -n memes get hpa
```

Delete the whole cluster:

```bash
kind delete cluster --name memes
```

## Updating the starter memes

The starter memes from `migrations/002_seed.sql` are loaded only into an empty table.
After changing that file, update the app and reload the data
(this deletes all memes, including ones added through the API):

```bash
git pull && ./scripts/kind-up.sh
```

```bash
kubectl -n memes exec postgres-0 -- psql -U memes -c "TRUNCATE memes"
```

```bash
kubectl -n memes rollout restart deployment/api
```

On startup the API sees an empty table and loads the memes again.

## Step 4. The daily relevance job (CronJob)

Every day at 03:00 (cluster time, UTC) Kubernetes starts a pod from the same image
with the `recalc` argument. It turns "trend" memes older than a year into "classic",
resets the Redis cache and exits. The age is set by `TREND_MAX_AGE_YEARS` in `deploy/k8s/cronjob.yaml`.

See the schedule and the last run:

```bash
kubectl -n memes get cronjob,jobs
```

Run the job right now without waiting for 03:00:

```bash
kubectl -n memes create job --from=cronjob/relevance relevance-manual
```

Read its log (it prints how many memes changed):

```bash
kubectl -n memes logs job/relevance-manual
```

To run it by hand again, delete the old job first: `kubectl -n memes delete job relevance-manual`.

## Step 5. Meme pictures (SeaweedFS)

Pictures are stored in SeaweedFS, an S3-compatible storage that runs in the cluster.
The API saves uploads there and serves them at `/images/...`; SeaweedFS itself is not exposed outside the cluster.
Its access keys come from `.env` (`S3_ACCESS_KEY`, `S3_SECRET_KEY`, letters and digits only):
the script puts them into the `meme-secrets` Secret for the API and into the `seaweedfs-s3` Secret,
which SeaweedFS reads as a file (`/etc/seaweedfs/s3.json`).

The SeaweedFS image is about 700 MB, so check free disk space first (`df -h /`).

Find the meme ID:

```bash
curl -s "localhost:8080/api/memes?q=doge" | grep -o '"id":[0-9]*'
```

Upload a picture you have the right to use (here `doge.jpg` for meme 15):

```bash
curl -X PUT localhost:8080/api/memes/15/image -H "Authorization: Bearer $ADMIN_TOKEN" --data-binary @doge.jpg
```

The API checks the file contents, not its name: only PNG, JPEG, GIF and WebP up to 5 MB are accepted.
Refresh the site: the picture replaces the colored cover. Uploading again replaces the picture.

If you change the storage keys in `.env`, rerun the script and restart SeaweedFS so it rereads them:
`kubectl -n memes rollout restart statefulset/seaweedfs`.

## Step 6. Load and autoscaling

In one terminal, watch the HPA and pods (the screen refreshes every 2 seconds):

```bash
watch -n 2 kubectl -n memes get hpa,pods
```

In another terminal, run the load test (no need to install k6, it runs in Docker):

```bash
docker run --rm -i --network host grafana/k6 run - < loadtest/k6.js
```

After a minute or two the CPU load goes above 60% and the HPA starts adding API pods (up to 8).
After the test ends, the extra pods are removed in about 5 minutes.

## Things to try next

- Delete an API pod (`kubectl -n memes delete pod <name>`) and watch the Deployment create a new one.
- Delete the Postgres pod: the data stays because it lives on a PersistentVolume.
- Stop Redis (`kubectl -n memes scale deploy/redis --replicas=0`): the site keeps working, just without the cache.
- Compare the k6 results with and without Redis.
- Change the password in `.env`, rerun the script and figure out why Postgres rejects it
  (hint: the password is set only when the database is first created).

Next steps for the project: meme images in MinIO, a CronJob that recalculates relevance,
access through Ingress or the Gateway API, metrics in Prometheus and Grafana, storing secrets outside the cluster.
