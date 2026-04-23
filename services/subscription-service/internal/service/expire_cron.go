package service

import (
	"context"
	"time"

	"github.com/vpn/subscription-service/internal/repository"
	vpnpb "github.com/vpn/shared/pkg/proto/vpn/v1"
	"go.uber.org/zap"
)

// ExpireInterval — как часто бежит cron. 10 минут — компромисс:
// юзер теряет VPN не позже чем через 10 мин после истечения.
const ExpireInterval = 10 * time.Minute

// ExpireCron — фоновая задача: раз в 10 минут помечает истёкшие
// подписки как expired и дёргает vpn-service.DisableVPNUser для каждой.
type ExpireCron struct {
	repo *repository.SubscriptionRepository
	vpn  vpnpb.VPNServiceClient
	log  *zap.Logger
}

func NewExpireCron(repo *repository.SubscriptionRepository, vpn vpnpb.VPNServiceClient, log *zap.Logger) *ExpireCron {
	return &ExpireCron{repo: repo, vpn: vpn, log: log}
}

// Run — блокирующий цикл. Запускается в отдельной горутине, завершается по ctx.
func (c *ExpireCron) Run(ctx context.Context) {
	c.log.Info("expire cron started", zap.Duration("interval", ExpireInterval))
	ticker := time.NewTicker(ExpireInterval)
	defer ticker.Stop()

	// Первый tick сразу — не ждём 10 минут на старте.
	c.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			c.log.Info("expire cron stopped")
			return
		case <-ticker.C:
			c.tick(ctx)
		}
	}
}

func (c *ExpireCron) tick(ctx context.Context) {
	userIDs, err := c.repo.ExpireOverdueSubscriptions(ctx)
	if err != nil {
		c.log.Error("expire cron: mark expired failed", zap.Error(err))
		return
	}
	if len(userIDs) == 0 {
		return
	}

	c.log.Info("expire cron: subscriptions expired", zap.Int("count", len(userIDs)))

	// Для каждого юзера дёргаем DisableVPNUser (удаление из Xray + БД).
	// vpn-service сам идемпотентен, так что повторный вызов для уже
	// отключённого юзера — no-op.
	for _, uid := range userIDs {
		if _, err := c.vpn.DisableVPNUser(ctx, &vpnpb.DisableVPNUserRequest{UserId: uid}); err != nil {
			c.log.Error("expire cron: DisableVPNUser failed",
				zap.Int64("user_id", uid), zap.Error(err))
			continue
		}
		c.log.Info("expire cron: vpn user disabled", zap.Int64("user_id", uid))
	}
}
