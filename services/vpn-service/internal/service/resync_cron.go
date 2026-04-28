package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/vpn/platform/pkg/xray"
	"github.com/vpn/vpn-service/internal/repository"
	"go.uber.org/zap"
)

// ResyncCron — фоновый сервис для гарантии что Xray-инстансы знают
// всех vpn_users из БД. Решает фундаментальную проблему: Xray хранит
// users in-memory, после рестарта контейнера clients[] обнуляется, и
// без явного `ResyncServer` пользователи получают rejected proxy/vless:
// "invalid request user id".
//
// Стратегия — два независимых тикера:
//
//  1. **Health-check (30с):** для каждого активного сервера делаем дешёвый
//     `xray.Ping`. Если был unhealthy → стал healthy = детектируем рестарт →
//     триггерим ResyncServer для восстановления clients[].
//  2. **Periodic safety net (1ч):** прогоняем ResyncServer для всех
//     активных серверов, независимо от health-state. Покрывает edge-кейсы
//     где health-check не зафиксировал транзишн (тихий рестарт, длинный
//     gRPC reconnect и т.п.).
//
// Оба пути идемпотентны — `ResyncServer` уже считает "already exists" как
// успех, так что noop-resync безопасен и дешёв.
type ResyncCron struct {
	repo   *repository.VPNRepository
	pool   *xray.Pool
	svc    *VPNService
	logger *zap.Logger

	mu          sync.Mutex
	lastHealthy map[int32]bool // server_id → healthy state на прошлой проверке
}

// HealthCheckInterval — частота ping-проверок. 30с — компромисс: окно
// до 30с после рестарта Xray, в течение которых юзеры будут получать
// reject'ы. Меньше слишком шумно, больше — длинное окно простоя.
const HealthCheckInterval = 30 * time.Second

// PeriodicResyncInterval — safety-net resync. 1ч покрывает любые edge-кейсы
// где health-check не зафиксировал транзишн.
const PeriodicResyncInterval = 1 * time.Hour

// PingTimeout — сколько ждём ответа от xray.Ping. Должен быть больше чем
// медианный RTT до самого далёкого сервера, но достаточно короткий чтобы
// healthy-сервера не задерживали проверку соседей.
const PingTimeout = 5 * time.Second

func NewResyncCron(repo *repository.VPNRepository, pool *xray.Pool, svc *VPNService, logger *zap.Logger) *ResyncCron {
	return &ResyncCron{
		repo:        repo,
		pool:        pool,
		svc:         svc,
		logger:      logger,
		lastHealthy: make(map[int32]bool),
	}
}

// Run — блокирующий цикл; запускается в горутине, останавливается при
// отмене ctx.
func (rc *ResyncCron) Run(ctx context.Context) {
	rc.logger.Info("resync cron started",
		zap.Duration("health_check", HealthCheckInterval),
		zap.Duration("periodic", PeriodicResyncInterval),
	)
	healthTicker := time.NewTicker(HealthCheckInterval)
	periodicTicker := time.NewTicker(PeriodicResyncInterval)
	defer healthTicker.Stop()
	defer periodicTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			rc.logger.Info("resync cron stopped")
			return
		case <-healthTicker.C:
			rc.checkHealth(ctx)
		case <-periodicTicker.C:
			rc.periodicResync(ctx)
		}
	}
}

// checkHealth — пробежать по всем активным серверам, пингануть каждый,
// и при транзишне UNHEALTHY → HEALTHY триггернуть ResyncServer.
func (rc *ResyncCron) checkHealth(ctx context.Context) {
	servers, err := rc.repo.ListServers(ctx, true)
	if err != nil {
		rc.logger.Warn("resync cron: list servers", zap.Error(err))
		return
	}

	for _, srv := range servers {
		addr := fmt.Sprintf("%s:%d", srv.XrayAPIHost, srv.XrayAPIPort)
		cli, err := rc.pool.GetOrConnect(ctx, srv.ID, addr)
		if err != nil {
			rc.markUnhealthy(srv.ID)
			rc.logger.Warn("resync cron: connect failed",
				zap.Int32("server_id", srv.ID),
				zap.String("addr", addr),
				zap.Error(err),
			)
			continue
		}

		pingCtx, cancel := context.WithTimeout(ctx, PingTimeout)
		err = cli.Ping(pingCtx)
		cancel()

		if err != nil {
			rc.markUnhealthy(srv.ID)
			rc.logger.Warn("resync cron: server unhealthy",
				zap.Int32("server_id", srv.ID),
				zap.String("name", srv.Name),
				zap.Error(err),
			)
			continue
		}

		// Healthy сейчас. Проверяем — не recovery ли это.
		if rc.markHealthyAndDetectRecovery(srv.ID) {
			rc.logger.Info("resync cron: server recovered, triggering resync",
				zap.Int32("server_id", srv.ID),
				zap.String("name", srv.Name),
			)
			res, err := rc.svc.ResyncServer(ctx, srv.ID)
			if err != nil {
				rc.logger.Error("resync cron: recovery resync failed",
					zap.Int32("server_id", srv.ID),
					zap.Error(err),
				)
				continue
			}
			rc.logger.Info("resync cron: recovery resync done",
				zap.Int32("server_id", srv.ID),
				zap.Int32("total", res.Total),
				zap.Int32("added", res.Added),
				zap.Int32("already", res.AlreadyExist),
				zap.Int32("failed", res.Failed),
			)
		}
	}
}

// markHealthyAndDetectRecovery — отмечает сервер как healthy и возвращает
// true ТОЛЬКО если на прошлой проверке он был unhealthy (т.е. это recovery).
//
// Если сервер впервые встречен (не в map) — recovery=false, так как
// startup-resync в app.go уже покрывает первоначальную инициализацию.
func (rc *ResyncCron) markHealthyAndDetectRecovery(id int32) (recovered bool) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	was, seen := rc.lastHealthy[id]
	rc.lastHealthy[id] = true
	return seen && !was
}

// markUnhealthy — отмечает сервер как unhealthy. На следующей recovery
// будет триггер ResyncServer.
func (rc *ResyncCron) markUnhealthy(id int32) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.lastHealthy[id] = false
}

// periodicResync — раз в час прогон ResyncServer для всех активных серверов.
// Защита от silent drift: если health-check почему-то не уловил рестарт
// (например gRPC connection восстановилась автоматом и Ping проходит, но
// clients[] на самом деле пустой) — этот тик заметит added>0 и должет.
func (rc *ResyncCron) periodicResync(ctx context.Context) {
	servers, err := rc.repo.ListServers(ctx, true)
	if err != nil {
		rc.logger.Warn("resync cron: periodic list servers", zap.Error(err))
		return
	}

	rc.logger.Info("resync cron: periodic safety-net starting",
		zap.Int("servers", len(servers)),
	)
	for _, srv := range servers {
		res, err := rc.svc.ResyncServer(ctx, srv.ID)
		if err != nil {
			rc.logger.Warn("resync cron: periodic resync failed",
				zap.Int32("server_id", srv.ID),
				zap.Error(err),
			)
			continue
		}
		// added > 0 = silent drift detected (clients[] был частично пустой).
		// Это серьёзный warn — следить за этим в логах.
		if res.Added > 0 {
			rc.logger.Warn("resync cron: periodic safety-net detected drift",
				zap.Int32("server_id", srv.ID),
				zap.String("name", srv.Name),
				zap.Int32("added", res.Added),
				zap.Int32("already", res.AlreadyExist),
			)
		} else {
			rc.logger.Info("resync cron: periodic safety-net clean",
				zap.Int32("server_id", srv.ID),
				zap.String("name", srv.Name),
				zap.Int32("already", res.AlreadyExist),
			)
		}
	}
}
