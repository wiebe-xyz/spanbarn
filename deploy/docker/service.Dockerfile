FROM golang:1.25-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
COPY sdks/go ./sdks/go
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=dev BUILD_TIME=unknown
RUN go build -ldflags "-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME}" -o /out/spanbarn ./cmd/spanbarn

FROM alpine:3.20 AS litestream
ARG LITESTREAM_VERSION=0.3.13
RUN apk add --no-cache ca-certificates wget && \
    wget -qO /tmp/litestream.tar.gz \
      "https://github.com/benbjohnson/litestream/releases/download/v${LITESTREAM_VERSION}/litestream-v${LITESTREAM_VERSION}-linux-amd64.tar.gz" && \
    tar -C /usr/local/bin -xzf /tmp/litestream.tar.gz litestream

FROM alpine:3.20

WORKDIR /app

RUN apk add --no-cache ca-certificates sqlite
RUN addgroup -S spanbarn && adduser -S spanbarn -G spanbarn

COPY --from=build /out/spanbarn /usr/local/bin/spanbarn
COPY --from=litestream /usr/local/bin/litestream /usr/local/bin/litestream
COPY deploy/docker/litestream.yml /etc/litestream.yml
COPY deploy/docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

RUN mkdir -p /var/lib/spanbarn && chown spanbarn:spanbarn /var/lib/spanbarn

USER spanbarn

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
