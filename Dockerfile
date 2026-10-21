# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/atto-skin-go ./cmd/atto-import-blessing

FROM alpine:3.23
LABEL org.opencontainers.image.title="Atto Skin" \
      org.opencontainers.image.description="OIDC Minecraft skin and Yggdrasil server" \
      org.opencontainers.image.source="https://github.com/zhyupe/atto-skin-go"
RUN apk add --no-cache ca-certificates \
    && addgroup -g 10001 atto \
    && adduser -D -u 10001 -G atto atto \
    && mkdir /data && chown atto:atto /data
COPY --from=build /out/atto-skin-go /usr/local/bin/atto-skin-go
COPY --from=build /out/atto-import-blessing /usr/local/bin/atto-import-blessing
USER 10001:10001
ENV DATA_DIR=/data LISTEN_ADDR=:8080
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/atto-skin-go"]
