FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -mod=vendor -ldflags="-w -s" -o /app/server ./cmd/server

FROM alpine:latest

RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=builder /app/server .

RUN mkdir -p /app/data

EXPOSE 8080

CMD ["/app/server"]
