ARG BUILD_REVISION=unknown
ARG IMAGE_VERSION=dev
ARG API_CONTRACT_VERSION=unknown

FROM node:24.17.0-alpine3.24@sha256:156b55f92e98ccd5ef49578a8cea0df4679826564bad1c9d4ef04462b9f0ded6 AS builder

WORKDIR /workspace

RUN corepack enable

COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY apps/web/package.json apps/web/package.json
RUN pnpm install --frozen-lockfile --filter @health/web...

COPY contracts/openapi.json contracts/openapi.json
COPY apps/web apps/web
RUN pnpm --filter @health/web build

FROM nginxinc/nginx-unprivileged:1.31.3-alpine3.24@sha256:a6c3ec0c0d249d68b0682df854d4a9e222b90fb607dc3fcf2f1d2fcbc85d347e

ARG BUILD_REVISION
ARG IMAGE_VERSION
ARG API_CONTRACT_VERSION

LABEL org.opencontainers.image.title="Health Dashboard frontend" \
      org.opencontainers.image.description="Static React client for the Health Dashboard API" \
      org.opencontainers.image.source="https://github.com/Dzarlax-AI/health_dashboard" \
      org.opencontainers.image.revision="${BUILD_REVISION}" \
      org.opencontainers.image.version="${IMAGE_VERSION}" \
      io.health-dashboard.api-contract-version="${API_CONTRACT_VERSION}" \
      io.health-dashboard.image-role="frontend"

COPY --chown=101:101 apps/web/container/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=builder --chown=101:101 /workspace/apps/web/dist/ /usr/share/nginx/html/

USER 101

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O - http://127.0.0.1:8080/healthz >/dev/null || exit 1
