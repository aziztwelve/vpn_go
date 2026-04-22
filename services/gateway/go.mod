module github.com/vpn/gateway

go 1.26.0

toolchain go1.26.2

require (
	github.com/go-chi/chi/v5 v5.2.0
	github.com/go-chi/cors v1.2.1
	github.com/joho/godotenv v1.5.1
	github.com/vpn/platform v0.0.0
	github.com/vpn/shared v0.0.0-00010101000000-000000000000
	go.uber.org/zap v1.27.0
	google.golang.org/grpc v1.79.3
)

require (
	github.com/golang-jwt/jwt/v5 v5.2.1 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/net v0.52.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/text v0.35.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251202230838-ff82c1b0f217 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace (
	github.com/vpn/platform => ../../platform
	github.com/vpn/shared => ../../shared
)
