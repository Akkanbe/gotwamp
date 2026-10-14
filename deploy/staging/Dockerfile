# syntax=docker/dockerfile:1
#
# gotwamp staging image: the binaries of deploy/docker/Dockerfile on Debian,
# with iproute2 (tc / netem), tcpdump and curl for experiments. Not for
# production: the distributed image is distroless (deploy/docker).
#
#   docker compose -f deploy/staging/docker-compose.yml build

ARG GO_VERSION=1.26

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=staging
ARG COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/ ./cmd/gotwampd ./cmd/gotwamp

FROM debian:12-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends iproute2 tcpdump curl iputils-ping ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/gotwampd /out/gotwamp /usr/bin/
COPY deploy/staging/entrypoint.sh /usr/local/bin/gotwampd-staging
RUN mkdir -p /run/gotwamp /var/lib/gotwamp /etc/gotwamp
ENV GOTWAMP_SOCKET=/run/gotwamp/gotwampd.sock
EXPOSE 862/tcp 862/udp 9862/tcp
# Healthy while the API answers, as the distributed image.
HEALTHCHECK --interval=10s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/usr/bin/gotwamp", "status", "--rpc-timeout", "3s"]
ENTRYPOINT ["/usr/local/bin/gotwampd-staging"]
