# syntax=docker/dockerfile:1
FROM docker.io/library/golang:1.26.5 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN test ! -e package.json \
    && test ! -e package-lock.json \
    && test ! -d node_modules \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath \
       -ldflags="-s -w -buildid=" -o /out/dashboard ./cmd/dashboard \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath \
       -ldflags="-s -w -buildid=" -o /out/homelab-host-agent ./cmd/host-agent \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath \
       -ldflags="-s -w -buildid=" -o /out/homelab-node-agent ./cmd/node-agent

# The installer extracts this static binary with rootless Podman. The stage is
# named so the host never needs a Go toolchain.
FROM scratch AS host-agent-export
COPY --from=build /out/homelab-host-agent /homelab-host-agent
ENTRYPOINT ["/homelab-host-agent"]

# Remote nodes extract this binary with rootless Podman. They do not need a Go
# toolchain and never expose an inbound management port.
FROM scratch AS node-agent-export
COPY --from=build /out/homelab-node-agent /homelab-node-agent
ENTRYPOINT ["/homelab-node-agent"]

FROM scratch AS dashboard
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/dashboard /dashboard
VOLUME ["/data"]
ENTRYPOINT ["/dashboard"]
