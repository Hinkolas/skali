FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -o /hello-build .

FROM scratch
COPY --from=builder /hello-build /hello-build
EXPOSE 8080
ENTRYPOINT ["/hello-build"]
