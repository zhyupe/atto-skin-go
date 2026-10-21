FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /atto-skin-go ./cmd/atto-skin-go

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -g 10001 atto && adduser -D -u 10001 -G atto atto && mkdir /data && chown atto:atto /data
COPY --from=build /atto-skin-go /usr/local/bin/atto-skin-go
USER atto
ENV DATA_DIR=/data LISTEN_ADDR=:8080
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/atto-skin-go"]
