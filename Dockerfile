# Этап 1: сборка. Здесь есть компилятор Go — образ большой, но он нужен только для сборки.
FROM golang:1.24-alpine AS build
WORKDIR /src
# Сначала только go.mod/go.sum: Docker закэширует скачанные зависимости,
# и при изменении кода они не будут скачиваться заново.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO_ENABLED=0 — статический бинарник, которому не нужны системные библиотеки.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /meme-o-pedia .

# Этап 2: итоговый образ. Только бинарник, без shell и пакетного менеджера (~15 МБ).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /meme-o-pedia /meme-o-pedia
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/meme-o-pedia"]
