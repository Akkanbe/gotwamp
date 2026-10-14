# syntax=docker/dockerfile:1
#
# gotwamp container image: gotwampd and the gotwamp CLI on distroless.
#
#   docker build -f deploy/docker/Dockerfile -t gotwamp .
#   docker run -d --name gotwampd -p 862:862/tcp -p 862:862/udp gotwamp
#   docker exec gotwampd gotwamp status
#
# The image runs as the distroless "nonroot" user. Docker lets processes in
# a container bind ports below 1024 (net.ipv4.ip_unprivileged_port_start=0),
# so port 862 needs no capability unless the container shares the host's
# network namespace; then add --cap-add NET_BIND_SERVICE and run as root, or
# move the listeners above 1024.

ARG GO_VERSION=1.26

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/usr/bin/ ./cmd/gotwampd ./cmd/gotwamp
# Writable runtime and state directories for the nonroot user (uid 65532).
RUN mkdir -p /out/run/gotwamp /out/var/lib/gotwamp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/usr/bin/ /usr/bin/
COPY --from=build --chown=65532:65532 /out/run/gotwamp /run/gotwamp
COPY --from=build --chown=65532:65532 /out/var/lib/gotwamp /var/lib/gotwamp
COPY deploy/docker/gotwampd.yaml /etc/gotwamp/gotwampd.yaml
ENV GOTWAMP_SOCKET=/run/gotwamp/gotwampd.sock
USER nonroot:nonroot
EXPOSE 862/tcp 862/udp 9862/tcp
# Healthy while the API answers; the image has no shell or curl, so the CLI
# asks over the socket (exit 3 when gotwampd does not answer).
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/bin/gotwamp", "status", "--rpc-timeout", "3s"]
ENTRYPOINT ["/usr/bin/gotwampd"]
CMD ["--config", "/etc/gotwamp/gotwampd.yaml"]
