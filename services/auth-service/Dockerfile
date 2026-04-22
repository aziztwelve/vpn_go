# syntax=docker/dockerfile:1.7
# -------- Build stage --------
FROM golang:1.26-alpine AS builder

WORKDIR /build

# Copy workspace files (we build from monorepo root, so COPY paths are relative to vpn_go/)
ENV GOWORK=off
COPY platform ./platform
COPY shared ./shared
COPY services/auth-service ./services/auth-service

WORKDIR /build/services/auth-service

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/auth-service ./cmd/main

# -------- Runtime stage --------
FROM alpine:3.20 AS runtime

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 app

USER app
WORKDIR /app

COPY --from=builder /out/auth-service /app/auth-service

EXPOSE 50060
ENTRYPOINT ["/app/auth-service"]
