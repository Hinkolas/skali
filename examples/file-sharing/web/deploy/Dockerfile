FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /file-sharing .

# The manifest builds this stage (build.target). Alpine rather than scratch:
# the application reaches the bucket's route over https in production,
# which needs the system CA roots.
FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates
COPY --from=builder /file-sharing /app/file-sharing
EXPOSE 8080
# No ENTRYPOINT: a manifest `command` replaces the image's arguments, so the
# full command line lives in one place (the manifest runs the same binary
# as the release command with `migrate up`).
CMD ["/app/file-sharing", "serve"]
