package service

import (
	"context"
	"time"

	"github.com/vpn/vpn-service/internal/repository"
	"go.uber.org/zap"
)

// LoadCronInterval — как часто пересчитываем load_percent.
// 60с даёт свежий view для UI балансировки; чаще — бесполезно.
const LoadCronInterval = 60 * time.Second

// LoadCron — фоновый обновлятель vpn_servers.load_percent.
//
// Формула: load_percent = COUNT(active_connections WHERE server_id AND
//   last_seen > NOW() - 5min) * 100 / server_max_connections, clamped в [0..100]
//
// Источник активности: active_connections.last_seen, который обновляется
// Heartbeat'ом (Этап 3) на основе Xray Stats API. Т.е. load отражает
// реально гонящий трафик, а не просто открытые записи.
type LoadCron struct {
	repo *repository.VPNRepository
	log  *zap.Logger
}

func NewLoadCron(repo *repository.VPNRepository, log *zap.Logger) *LoadCron {
	return &LoadCron{repo: repo, log: log}
}

// Run блокирующий, запускается в горутине. Завершается по ctx.Done.
func (c *LoadCron) Run(ctx context.Context) {
	c.log.Info("load cron started", zap.Duration("interval", LoadCronInterval))
	ticker := time.NewTicker(LoadCronInterval)
	defer ticker.Stop()

	c.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			c.log.Info("load cron stopped")
			return
		case <-ticker.C:
			c.tick(ctx)
		}
	}
}

func (c *LoadCron) tick(ctx context.Context) {
	ids, err := c.repo.ListActiveServerIDs(ctx)
	if err != nil {
		c.log.Error("load cron: list servers failed", zap.Error(err))
		return
	}
	for _, id := range ids {
		if err := c.repo.UpdateServerLoad(ctx, id); err != nil {
			c.log.Error("load cron: update server load failed",
				zap.Int32("server_id", id), zap.Error(err))
		}
	}
}
