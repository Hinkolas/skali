# The skalid control-plane image. Alpine (not scratch) on purpose: the
# local bundle's bootstrap user Job pipes a generated password into
# `skalid user create --password-stdin` through a shell.
FROM golang:1.26.8-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /skalid ./cmd/skalid

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /skalid /usr/local/bin/skalid
EXPOSE 7070
ENTRYPOINT ["skalid"]
