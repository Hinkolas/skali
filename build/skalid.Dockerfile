# Build the static UI once; the runtime contains only the Go binary.
FROM node:22-alpine AS studio
WORKDIR /src
COPY studio/package.json studio/package-lock.json ./studio/
RUN npm ci --prefix studio
COPY studio ./studio
COPY scripts/embed-studio.mjs ./scripts/embed-studio.mjs
RUN npm run build --prefix studio && node scripts/embed-studio.mjs

# The skalid control-plane image. Alpine (not scratch) on purpose: the
# local bundle's bootstrap user Job pipes a generated password into
# `skalid user create --password-stdin` through a shell.
FROM golang:1.26.9-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=studio /src/internal/studio/dist ./internal/studio/dist
ARG VERSION=v0.0.0-dev
RUN CGO_ENABLED=0 go build -ldflags "-X github.com/Hinkolas/skali/internal/version.Version=${VERSION}" -o /skalid ./cmd/skalid

# Pinned by digest (the multi-arch index); bump the tag and digest together.
FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates
COPY --from=builder /skalid /usr/local/bin/skalid
EXPOSE 7070
ENTRYPOINT ["skalid"]
