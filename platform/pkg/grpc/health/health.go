// Package health implements the gRPC Health Checking Protocol (gRPC Health v1)
// with optional dynamic checks (database ping, etc).
//
// Usage:
//
//	// Минимум — статический SERVING:
//	health.RegisterService(grpcServer)
//
//	// С пингом БД (если БД не отвечает — статус NOT_SERVING):
//	health.RegisterServiceWithChecks(grpcServer, func(ctx context.Context) error {
//	    return db.Ping(ctx)
//	})
package health

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// CheckFunc — динамическая проверка зависимости (БД, кэш и т.п.).
// Возвращает nil если зависимость доступна, иначе error.
type CheckFunc func(ctx context.Context) error

// Server implements the gRPC Health Checking Protocol.
// Если есть dynamic checks — каждая проверка должна вернуть nil,
// иначе Check возвращает NOT_SERVING.
type Server struct {
	grpc_health_v1.UnimplementedHealthServer
	checks []CheckFunc
	// checkTimeout — таймаут на каждую проверку. Если 0, дефолт 1с.
	checkTimeout time.Duration
}

// New создаёт Health-сервер с указанными чеками.
// Без аргументов — всегда SERVING.
func New(checks ...CheckFunc) *Server {
	return &Server{
		checks:       checks,
		checkTimeout: time.Second,
	}
}

// WithTimeout — таймаут на каждый чек.
func (s *Server) WithTimeout(t time.Duration) *Server {
	s.checkTimeout = t
	return s
}

// Check выполняет все зарегистрированные чеки. Если хоть один упал — NOT_SERVING.
func (s *Server) Check(ctx context.Context, _ *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	if s.run(ctx) {
		return &grpc_health_v1.HealthCheckResponse{
			Status: grpc_health_v1.HealthCheckResponse_SERVING,
		}, nil
	}
	return &grpc_health_v1.HealthCheckResponse{
		Status: grpc_health_v1.HealthCheckResponse_NOT_SERVING,
	}, nil
}

// Watch — простая реализация: одно сообщение и держим стрим.
// Для production-grade watch'а нужно периодически перепроверять, но
// нам пока достаточно push-on-connect.
func (s *Server) Watch(_ *grpc_health_v1.HealthCheckRequest, stream grpc_health_v1.Health_WatchServer) error {
	status := grpc_health_v1.HealthCheckResponse_SERVING
	if !s.run(stream.Context()) {
		status = grpc_health_v1.HealthCheckResponse_NOT_SERVING
	}
	return stream.Send(&grpc_health_v1.HealthCheckResponse{Status: status})
}

// run выполняет все чеки последовательно с таймаутом. true если все OK.
func (s *Server) run(parent context.Context) bool {
	for _, c := range s.checks {
		ctx, cancel := context.WithTimeout(parent, s.checkTimeout)
		err := c(ctx)
		cancel()
		if err != nil {
			return false
		}
	}
	return true
}

// RegisterService регистрирует static SERVING-сервер (для совместимости).
// Эквивалент `health.New().Register(s)`.
func RegisterService(s *grpc.Server) {
	New().Register(s)
}

// RegisterServiceWithChecks регистрирует health-сервер с заданными чеками.
func RegisterServiceWithChecks(s *grpc.Server, checks ...CheckFunc) {
	New(checks...).Register(s)
}

// Register — instance-метод, удобен когда хочется настроить таймаут перед регистрацией.
func (s *Server) Register(g *grpc.Server) {
	grpc_health_v1.RegisterHealthServer(g, s)
}
