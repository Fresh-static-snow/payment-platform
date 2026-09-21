# syntax=docker/dockerfile:1.7

FROM golang:1.27-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

ARG SERVICE=payment-api
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    test -f "cmd/${SERVICE}/main.go" \
    && CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
       go build -trimpath -ldflags="-s -w" -o /out/service "./cmd/${SERVICE}"

FROM alpine:3.22 AS runtime

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 app \
    && adduser -S -D -H -u 10001 -G app app

WORKDIR /app
COPY --from=builder --chown=app:app /out/service /app/service

USER 10001:10001
ENTRYPOINT ["/app/service"]
