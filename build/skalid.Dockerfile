# Build the static UI once; the runtime contains only the Go binary.
FROM node:22-alpine AS web
WORKDIR /src
COPY web/package.json web/package-lock.json ./web/
RUN npm ci --prefix web
COPY web ./web
COPY scripts/embed-web.mjs ./scripts/embed-web.mjs
RUN npm run build --prefix web && node scripts/embed-web.mjs

# The skalid control-plane image. Alpine (not scratch) on purpose: the
# local bundle's bootstrap user Job pipes a generated password into
# `skalid user create --password-stdin` through a shell.
FROM golang:1.26.8-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 go build -o /skalid ./cmd/skalid

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /skalid /usr/local/bin/skalid
EXPOSE 7070
ENTRYPOINT ["skalid"]
