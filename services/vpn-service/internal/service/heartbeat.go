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

// HeartbeatInterval — как часто опрашиваем Xray Stats API.
// 60с — компромисс: реагируем на отключения в течение ~6 минут
// (1 опрос + 5 минут окна), но не забиваем Xray.
const HeartbeatInterval = 60 * time.Second

// Heartbeat опрашивает Xray Stats API для всех vpn_users по ВСЕМ активным
// серверам и обновляет active_connections.last_seen когда суммарный трафик
// юзера вырос (хоть на одном сервере).
//
// Multi-server: один юзер с одним UUID прописан во все Xray-инстансы. Чтобы
// понять что он "активен", нужно сложить uplink+downlink по всем серверам и
// сравнить с предыдущим значением. Если хоть где-то трафик вырос — last_seen
// обновляется (юзер реально пользуется VPN).
//
// Stream-stats хранится в памяти Heartbeat'а — сравнивается с предыдущим
// значением. Мы НЕ делаем reset=true на Xray, чтобы не сломать будущую
// статистику по биллингу.
type Heartbeat struct {
	repo     *repository.VPNRepository
	pool     *xray.Pool
	logger   *zap.Logger
	mu       sync.Mutex
	prevSeen map[string]int64 // email → суммарный uplink+downlink со всех серверов
}

func NewHeartbeat(repo *repository.VPNRepository, pool *xray.Pool, logger *zap.Logger) *Heartbeat {
	return &Heartbeat{
		repo:     repo,
		pool:     pool,
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
//
// Алгоритм multi-server:
//  1. ListServers(active=true) один раз за tick.
//  2. Для каждого юзера итерируем по серверам, суммируем uplink+downlink.
//     Если на каком-то сервере stats недоступен — пропускаем (юзер мог не
//     ходить туда, или сервер временно недоступен — не ошибка).
//  3. Сравниваем сумму с прошлой → если выросла, обновляем last_seen.
func (h *Heartbeat) tick(ctx context.Context) {
	users, err := h.repo.ListAllVPNUsers(ctx)
	if err != nil {
		h.logger.Error("heartbeat: list vpn users", zap.Error(err))
		return
	}

	servers, err := h.repo.ListServers(ctx, true)
	if err != nil {
		h.logger.Error("heartbeat: list active servers", zap.Error(err))
		return
	}

	updated := 0
	for _, u := range users {
		var total int64
		for _, srv := range servers {
			cli := h.pool.Get(srv.ID)
			if cli == nil {
				// Сервер ещё не в пуле (warm-up не дошёл / новый сервер) —
				// пробуем lazy-connect. Ошибка коннекта здесь не fatal,
				// просто пропустим этот сервер для текущего юзера.
				addr := serverAddr(srv.XrayAPIHost, srv.XrayAPIPort)
				cli, err = h.pool.GetOrConnect(ctx, srv.ID, addr)
				if err != nil {
					h.logger.Debug("heartbeat: server unreachable, skip",
						zap.Int32("server_id", srv.ID),
						zap.String("addr", addr),
						zap.Error(err),
					)
					continue
				}
			}
			stats, err := cli.GetUserStats(ctx, u.Email, false)
			if err != nil {
				// "no data" нормально — юзер не ходил через этот сервер.
				h.logger.Debug("heartbeat: get stats failed",
					zap.Int32("server_id", srv.ID),
					zap.String("email", u.Email),
					zap.Error(err))
				continue
			}
			total += stats.Uplink + stats.Downlink
		}

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
		h.logger.Info("heartbeat tick",
			zap.Int("users_checked", len(users)),
			zap.Int("servers_checked", len(servers)),
			zap.Int("refreshed", updated))
	}
}

// serverAddr — формат "host:port" для xray gRPC API. Возвращает то же что
// service.xrayClientForServer делает, но heartbeat пакет не имеет доступа
// к приватному helper'у — копируем строкой.
func serverAddr(host string, port int32) string {
	return fmt.Sprintf("%s:%d", host, port)
}
