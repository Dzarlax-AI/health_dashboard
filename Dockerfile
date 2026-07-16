FROM golang:1.25-alpine3.22@sha256:65b4400aee0927412e9ed791a11893273a49d55df24841f7599660fb80dae464 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-w -s" -o /app/server ./cmd/server \
    && CGO_ENABLED=0 go build -ldflags="-w -s" -o /app/tenant_isolation ./cmd/tenant_isolation

FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce

RUN apk add --no-cache ca-certificates \
    && addgroup -S health \
    && adduser -S -G health -h /app health

WORKDIR /app
COPY --from=builder --chown=health:health /app/server /app/tenant_isolation ./

RUN mkdir -p /app/data && chown health:health /app/data

USER health

EXPOSE 8080

CMD ["/app/server"]
