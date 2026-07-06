# syntax=docker/dockerfile:1
# Dev-cluster node image: skalid + its own Docker engine (DinD). One image for
# every node; ROLE (master|worker) picks the behavior at runtime — see
# entrypoint.sh. Build context is the repo root (compose sets it).

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev-cluster
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -buildvcs=false \
    -ldflags "-X github.com/Hinkolas/skali/internal/version.Version=${VERSION}" \
    -o /out/skalid ./cmd/skalid

FROM docker:28-dind
RUN apk add --no-cache curl jq
COPY --from=build /out/skalid /usr/local/bin/skalid
COPY deploy/dev-cluster/entrypoint.sh /usr/local/bin/skali-node
RUN chmod +x /usr/local/bin/skali-node
ENTRYPOINT ["skali-node"]
