# syntax=docker/dockerfile:1
# Barrage demo stack images.
#
# One Dockerfile, three runnable targets (compose selects via build.target):
#   barrel-barrage, ...demoserver, ...seeddb. All binaries are static
# (CGO_ENABLED=0, same as .github/workflows/build.yml) so the runtime
# images stay tiny alpine shells with just ca-certificates.

FROM golang:1.25-alpine AS deps
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

FROM deps AS build-all
COPY . .
RUN set -eux; \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/barrage ./cmd/barrage; \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/demoserver ./cmd/demoserver; \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/seeddb ./cmd/seeddb

FROM alpine:3.21 AS barrage
RUN apk add --no-cache ca-certificates
COPY --from=build-all /out/barrage /usr/local/bin/barrage
ENTRYPOINT ["barrage"]

FROM alpine:3.21 AS demoserver
RUN apk add --no-cache ca-certificates
COPY --from=build-all /out/demoserver /usr/local/bin/demoserver
EXPOSE 8080
ENTRYPOINT ["demoserver"]

FROM alpine:3.21 AS seeddb
RUN apk add --no-cache ca-certificates
COPY --from=build-all /out/seeddb /usr/local/bin/seeddb
ENTRYPOINT ["seeddb"]