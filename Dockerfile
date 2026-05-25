# syntax=docker/dockerfile:1

FROM golang:1.26-bookworm AS builder

ARG TARGETOS
ARG TARGETARCH
ARG GOPROXY
ENV GOOS=$TARGETOS \
    GOARCH=$TARGETARCH \
    CGO_ENABLED=0 \
    GO111MODULE=on

WORKDIR /src

COPY go.* ./
RUN go mod download

COPY . ./
RUN go build -trimpath -ldflags="-s -w" -o /out/clickhouse-bulk .

# Layout + CA bundle for distroless (static-debian12 includes certs; prep only sets ownership).
FROM alpine:3.21 AS runtime-prep

RUN mkdir -p /app/dumps /app/dumps-bkp /app/journal \
    && chown -R 65532:65532 /app

WORKDIR /app
COPY --from=builder --chown=65532:65532 /out/clickhouse-bulk .
COPY --chown=65532:65532 config.sample.json .

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=runtime-prep --chown=nonroot:nonroot /app/ /app/

EXPOSE 8124
VOLUME ["/app/dumps", "/app/dumps-bkp", "/app/journal"]

LABEL org.opencontainers.image.title="clickhouse-bulk" \
      org.opencontainers.image.description="HTTP bulk proxy for ClickHouse" \
      org.opencontainers.image.source="https://github.com/itcrow/clickhouse-bulk"

ENTRYPOINT ["/app/clickhouse-bulk"]
CMD ["-config=/app/config.sample.json"]
