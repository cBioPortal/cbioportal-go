# syntax=docker/dockerfile:1
#
# cbioportal-go: the API server, the importer and the built cBioPortal frontend.
# The frontend and Go stages run on the build platform; Go cross-compiles for
# the target, so a multi-arch build needs no emulation for the heavy steps.

ARG ALPINE_VERSION=3.24.2

FROM --platform=$BUILDPLATFORM node:22.18.0 AS frontend
# git-revision-webpack-plugin reads the commit and `git describe` output; the
# submodule's .git is not in the build context, so a shim answers with the
# values passed as build args.
ARG FRONTEND_COMMIT=unknown
ARG FRONTEND_VERSION=unknown
RUN printf '#!/bin/sh\ncase "$*" in\n  "rev-parse HEAD") echo "%s" ;;\n  "describe --always --tags --dirty") echo "%s" ;;\n  *) exec /usr/bin/git "$@" ;;\nesac\n' \
      "$FRONTEND_COMMIT" "$FRONTEND_VERSION" > /usr/local/bin/git \
 && chmod +x /usr/local/bin/git \
 && corepack enable
WORKDIR /src/frontend
COPY frontend/ ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store,sharing=locked \
    COREPACK_ENABLE_DOWNLOAD_PROMPT=0 pnpm install --frozen-lockfile
RUN pnpm run buildAll

# The built frontend. CI passes a cached build as this stage
# (--build-context frontend-dist=frontend/dist), which skips the build above.
FROM scratch AS frontend-dist
COPY --from=frontend /src/frontend/dist /

FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS go
ARG TARGETOS
ARG TARGETARCH
# Version stamp for builds without .git, e.g. "6b38ed6 (2026-10-02T21:00:00Z)".
ARG VERSION=""
# Full git commit, served as /api/info's gitCommitId.
ARG COMMIT=""
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/ cmd/
COPY internal/ internal/
COPY ops/ ops/
COPY rewrite/ rewrite/
# The API, the importer, cBioPortal.org's job runner (cbioportal-ops) and
# the Java-migration tools (cbioportal-rewrite-test).
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X 'github.com/cbioportal/cbioportal-go/internal/buildinfo.stamp=${VERSION}' -X 'github.com/cbioportal/cbioportal-go/internal/buildinfo.commit=${COMMIT}'" -o /out/ \
      ./cmd/cbioportal-api ./cmd/cbioportal-import ./ops/cbioportal.org/cmd/cbioportal-ops ./rewrite/cmd/cbioportal-rewrite-test

# cbioportal-core's study validator (scripts/importer), run by the hub's
# import job. To bump it, set CBIOPORTAL_CORE_REF to a newer commit of
# cBioPortal/cbioportal-core's main branch and check that its
# requirements.txt needs nothing the Python packages below lack.
FROM alpine:${ALPINE_VERSION} AS validator
ARG CBIOPORTAL_CORE_REF=61438ff5cea71f361ad938cafe4b5bb86d604180
RUN apk add --no-cache python3 py3-requests py3-yaml py3-jinja2 py3-pip curl
RUN mkdir -p /opt/cbioportal-core \
 && curl -fsSL "https://codeload.github.com/cBioPortal/cbioportal-core/tar.gz/${CBIOPORTAL_CORE_REF}" \
    | tar -xz -C /opt/cbioportal-core --strip-components=1 \
      "cbioportal-core-${CBIOPORTAL_CORE_REF}/scripts/importer" "cbioportal-core-${CBIOPORTAL_CORE_REF}/requirements.txt" \
 && echo "${CBIOPORTAL_CORE_REF}" > /opt/cbioportal-core/REF \
 && pip install --no-deps --no-compile --target /opt/cbioportal-core/site dsnparse==0.1.15
ENV PYTHONPATH=/opt/cbioportal-core/site
RUN python3 /opt/cbioportal-core/scripts/importer/validateData.py --help >/dev/null

FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache bash git git-lfs curl jq openssl ca-certificates \
    python3 py3-requests py3-yaml py3-jinja2 \
 && addgroup -g 10001 cbioportal \
 && adduser -D -u 10001 -G cbioportal -h /home/cbioportal cbioportal
COPY --from=go /out/ /usr/local/bin/
COPY --from=frontend-dist / /app/frontend
COPY --from=validator /opt/cbioportal-core /opt/cbioportal-core
# Deployment scripts and configuration: the generic ones and cBioPortal.org's
# own, side by side under /app/deploy where the cluster manifests expect them.
COPY deploy/ /app/deploy/
COPY ops/cbioportal.org/*.sh ops/cbioportal.org/*.properties ops/cbioportal.org/*.json ops/cbioportal.org/*.txt /app/deploy/
WORKDIR /app
USER 10001:10001
ENV PORT=8080 FRONTEND_DIR=/app/frontend PYTHONPATH=/opt/cbioportal-core/site
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/cbioportal-api"]
