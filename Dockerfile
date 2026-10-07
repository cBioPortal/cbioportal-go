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

FROM --platform=$BUILDPLATFORM golang:1.26.8-alpine AS go
ARG TARGETOS
ARG TARGETARCH
# Version stamp for builds without .git, e.g. "6b38ed6 (2026-10-02T21:00:00Z)".
ARG VERSION=""
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/ cmd/
COPY internal/ internal/
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X 'github.com/cbioportal/cbioportal-go/internal/buildinfo.stamp=${VERSION}' -X 'github.com/cbioportal/cbioportal-go/internal/buildinfo.commit=${COMMIT}'" -o /out/cbioportal-api ./cmd/cbioportal-api \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X 'github.com/cbioportal/cbioportal-go/internal/buildinfo.stamp=${VERSION}' -X 'github.com/cbioportal/cbioportal-go/internal/buildinfo.commit=${COMMIT}'" -o /out/cbioportal-import ./cmd/cbioportal-import

FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache bash git git-lfs curl jq openssl ca-certificates \
 && addgroup -g 10001 cbioportal \
 && adduser -D -u 10001 -G cbioportal -h /home/cbioportal cbioportal
COPY --from=go /out/cbioportal-api /out/cbioportal-import /usr/local/bin/
COPY --from=frontend /src/frontend/dist /app/frontend
COPY deploy/ /app/deploy/
WORKDIR /app
USER 10001:10001
ENV PORT=8080 FRONTEND_DIR=/app/frontend
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/cbioportal-api"]
