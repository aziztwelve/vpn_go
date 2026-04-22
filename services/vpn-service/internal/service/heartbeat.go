package service

import (
	"context"
	"sync"
	"time"

	"github.com/vpn/platform/pkg/xray"
	"github.com/vpn/vpn-service/internal/repository"
	"go.uber.org/zap"
)

// HeartbeatInterval — как часто опрашиваем Xray Stats API.
// 60с — компромисс: реагируем на отключения в течение ~6 минут
// (1 опрос + 5 минут окна), но не забиваем Xray.
const HeartbeatInterval = 60 * time.Second

// Heartbeat опрашивает Xray Stats API для всех vpn_users и обновляет
// active_connections.last_seen когда трафик юзера вырос.
//
// Stream-stats (uplink+downlink) хранится в памяти Heartbeat'а — сравнивается
// с предыдущим значением. Мы НЕ делаем reset=true на Xray, чтобы не сломать
// будущую статистику по биллингу.
type Heartbeat struct {
	repo     *repository.VPNRepository
	xray     *xray.Client
	logger   *zap.Logger
	mu       sync.Mutex
	prevSeen map[string]int64 // email → последнее сумма uplink+downlink
}

func NewHeartbeat(repo *repository.VPNRepository, xrayCli *xray.Client, logger *zap.Logger) *Heartbeat {
	return &Heartbeat{
		repo:     repo,
		xray:     xrayCli,
		logger:   logger,
		prevSeen: make(map[string]int64),
	}
}

// Run — блокирующий цикл опроса, завершается при отмене ctx.
// Обычно запускается в отдельной горутине.
func (h *Heartbeat) Run(ctx context.Context) {
	h.logger.Info("heartbeat started", zap.Duration("interval", HeartbeatInterval))
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	// Первый tick сразу — чтобы не ждать минуту впустую.
	h.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			h.logger.Info("heartbeat stopped")
			return
		case <-ticker.C:
			h.tick(ctx)
		}
	}
}

// tick — одна итерация опроса. Свои ошибки не роняют цикл, только логируем.
func (h *Heartbeat) tick(ctx context.Context) {
	users, err := h.repo.ListAllVPNUsers(ctx)
	if err != nil {
		h.logger.Error("heartbeat: list vpn users", zap.Error(err))
		return
	}

	updated := 0
	for _, u := range users {
		stats, err := h.xray.GetUserStats(ctx, u.Email, false)
		if err != nil {
			// Если у юзера не было трафика вообще — Xray может вернуть
			// ошибку "no data". Это нормально, продолжаем.
			h.logger.Debug("heartbeat: get stats failed",
				zap.String("email", u.Email), zap.Error(err))
			continue
		}
		total := stats.Uplink + stats.Downlink

		h.mu.Lock()
		prev, seen := h.prevSeen[u.Email]
		h.prevSeen[u.Email] = total
		h.mu.Unlock()

		// При первом обращении (seen=false) сохраняем baseline, но last_seen
		// не обновляем — иначе только-что созданные юзеры будут "активны"
		// даже если ничего не делали.
		if !seen {
			continue
		}

		if total > prev {
			if err := h.repo.UpdateLastSeenByVPNUser(ctx, u.ID); err != nil {
				h.logger.Error("heartbeat: update last_seen",
					zap.Int64("vpn_user_id", u.ID), zap.Error(err))
				continue
			}
			updated++
			h.logger.Debug("heartbeat: traffic grew, refreshed last_seen",
				zap.String("email", u.Email),
				zap.Int64("delta_bytes", total-prev),
			)
		}
	}

	if updated > 0 {
		h.logger.Info("heartbeat tick", zap.Int("users_checked", len(users)), zap.Int("refreshed", updated))
	}
}
