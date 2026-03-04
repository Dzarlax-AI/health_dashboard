FROM golang:1.22-alpine AS builder
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY . .
RUN CGO_ENABLED=1 go build -mod=vendor -o /app/server ./cmd/server && \
    CGO_ENABLED=1 go build -mod=vendor -o /app/downsample ./cmd/downsample

FROM alpine:latest

RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=builder /app/server .
COPY --from=builder /app/downsample .

RUN mkdir -p /app/data

EXPOSE 8080

CMD ["/app/server"]
