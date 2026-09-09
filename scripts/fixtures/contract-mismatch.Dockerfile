ARG BASE_IMAGE=health-frontend:local
FROM ${BASE_IMAGE}

ARG API_CONTRACT_VERSION
LABEL io.health-dashboard.api-contract-version="${API_CONTRACT_VERSION}"
