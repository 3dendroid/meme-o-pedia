# Stage 1: build. This image has the Go compiler; it is large but only needed for the build.
FROM golang:1.24-alpine AS build
WORKDIR /src
# Copy only go.mod/go.sum first: Docker caches the downloaded dependencies,
# so they are not downloaded again when only the code changes.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO_ENABLED=0 produces a static binary that needs no system libraries.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /meme-o-pedia .

# Stage 2: final image. Just the binary, no shell or package manager (~20 MB).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /meme-o-pedia /meme-o-pedia
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/meme-o-pedia"]
