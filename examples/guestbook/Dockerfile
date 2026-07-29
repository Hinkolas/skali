FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum main.go ./
RUN go mod download
RUN CGO_ENABLED=0 go build -o /guestbook .

FROM scratch
COPY --from=builder /guestbook /guestbook
EXPOSE 8080
ENTRYPOINT ["/guestbook"]
