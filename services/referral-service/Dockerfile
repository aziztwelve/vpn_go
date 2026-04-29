# syntax=docker/dockerfile:1.7
# -------- Build stage --------
FROM golang:1.26-alpine AS builder

WORKDIR /build

ENV GOWORK=off
COPY platform ./platform
COPY shared ./shared
COPY services/referral-service ./services/referral-service

WORKDIR /build/services/referral-service

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/referral-service ./cmd/main

# -------- Runtime stage --------
FROM alpine:3.20 AS runtime

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 app

USER app
WORKDIR /app

COPY --from=builder /out/referral-service /app/referral-service

EXPOSE 50064
ENTRYPOINT ["/app/referral-service"]
