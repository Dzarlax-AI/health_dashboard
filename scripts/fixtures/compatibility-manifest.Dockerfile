FROM scratch

ARG PAIR_REVISION
ARG API_CONTRACT_VERSION
ARG BACKEND_IMAGE
ARG BACKEND_DIGEST
ARG BACKEND_REVISION
ARG FRONTEND_IMAGE
ARG FRONTEND_DIGEST
ARG FRONTEND_REVISION

LABEL org.opencontainers.image.title="Health Dashboard compatibility pair" \
      org.opencontainers.image.revision="${PAIR_REVISION}" \
      io.health-dashboard.image-role="compatibility-pair" \
      io.health-dashboard.api-contract-version="${API_CONTRACT_VERSION}" \
      io.health-dashboard.pair-revision="${PAIR_REVISION}" \
      io.health-dashboard.backend-image="${BACKEND_IMAGE}" \
      io.health-dashboard.backend-digest="${BACKEND_DIGEST}" \
      io.health-dashboard.backend-revision="${BACKEND_REVISION}" \
      io.health-dashboard.frontend-image="${FRONTEND_IMAGE}" \
      io.health-dashboard.frontend-digest="${FRONTEND_DIGEST}" \
      io.health-dashboard.frontend-revision="${FRONTEND_REVISION}"

COPY compatibility-manifest.json /compatibility.json
