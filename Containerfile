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
       -ldflags="-s -w -buildid=" -o /out/dashboard ./cmd/dashboard

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/dashboard /dashboard
VOLUME ["/data"]
ENTRYPOINT ["/dashboard"]
