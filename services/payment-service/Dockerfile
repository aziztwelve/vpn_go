# syntax=docker/dockerfile:1.7
# -------- Build stage --------
FROM golang:1.26-alpine AS builder

WORKDIR /build

ENV GOWORK=off
COPY platform ./platform
COPY shared ./shared
COPY services/payment-service ./services/payment-service

WORKDIR /build/services/payment-service

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/payment-service ./cmd/main

# -------- Runtime stage --------
FROM alpine:3.20 AS runtime

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 app

USER app
WORKDIR /app

COPY --from=builder /out/payment-service /app/payment-service

EXPOSE 50063
ENTRYPOINT ["/app/payment-service"]
