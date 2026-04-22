# syntax=docker/dockerfile:1.7
# -------- Build stage --------
FROM golang:1.26-alpine AS builder

WORKDIR /build

ENV GOWORK=off
COPY platform ./platform
COPY shared ./shared
COPY services/subscription-service ./services/subscription-service

WORKDIR /build/services/subscription-service

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/subscription-service ./cmd/main

# -------- Runtime stage --------
FROM alpine:3.20 AS runtime

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 app

USER app
WORKDIR /app

COPY --from=builder /out/subscription-service /app/subscription-service

EXPOSE 50061
ENTRYPOINT ["/app/subscription-service"]
