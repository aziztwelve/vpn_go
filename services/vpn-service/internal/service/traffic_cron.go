package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vpn/platform/pkg/xray"
	"github.com/vpn/vpn-service/internal/repository"
	"go.uber.org/zap"
)

// TrafficCronInterval — как часто собираем дельты с xray.
//
// 5 минут = компромисс:
//   - cap-enforcement: при лимите 200GB/мес окно 5 мин = максимум "льготного"
//     пере-использования ≈ 50 MB на активного юзера (100 Mbit линк), приемлемо;
//   - retention-сегментация: минимальное окно "юзер активен" — 5 мин, что
//     даёт корректные бакеты "был ли трафик в последние 24h";
//   - нагрузка на Xray: 1 RPC на сервер за тик (bulk QueryStats), ≈12 RPC/час
//     при 3 серверах — пренебрежимо.
const TrafficCronInterval = 5 * time.Minute

// UserEmailPrefix — префикс в xray stats "user>>>{email}>>>traffic>>>..."
// соответствующий нашему шаблону email (см. CreateVPNUser в service/vpn.go).
// Реальный email формируется как fmt.Sprintf("user%d@vpn.local", userID).
// Парсим обратно: "userN@vpn.local" → N.
const (
	userEmailPrefix = "user"
	userEmailSuffix = "@vpn.local"
)

// TrafficCron периодически забирает дельты трафика из xray stats со всех
// активных серверов и пишет в traffic_samples + обновляет денормализованные
// поля users.{first_connection_at, last_traffic_at}.
//
// Multi-server model: один vpn_user с одним UUID прописан во все серверы
// (см. docs/services/multi-server.md). Юзер может подключиться к любому
// из N серверов — xray на этом конкретном сервере накопит статистику.
// Мы ходим bulk'ом в каждый xray и складываем дельты per (vpn_user, server).
//
// reset=true: после чтения счётчик в xray обнуляется. Следующий тик видит
// только новый инкремент. Гарантия от double-counting + защита от рестарта
// xray (перезапустился → счётчик 0 в xray, мы получим 0 дельту, не минус).
type TrafficCron struct {
	repo   *repository.VPNRepository
	pool   *xray.Pool
	logger *zap.Logger
}

func NewTrafficCron(repo *repository.VPNRepository, pool *xray.Pool, logger *zap.Logger) *TrafficCron {
	return &TrafficCron{repo: repo, pool: pool, logger: logger}
}

func (c *TrafficCron) Run(ctx context.Context) {
	c.logger.Info("traffic cron started", zap.Duration("interval", TrafficCronInterval))
	ticker := time.NewTicker(TrafficCronInterval)
	defer ticker.Stop()

	// Первый tick с небольшой задержкой, чтобы дать warm-up пула доделаться
	// и чтобы xray-рестарты успели стабилизироваться. 30с — эвристика,
	// ничего критичного не сломается при раннем тике.
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}
	c.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("traffic cron stopped")
			return
		case <-ticker.C:
			c.tick(ctx)
		}
	}
}

// tick — одна итерация опроса. Ошибки отдельного сервера логируются,
// но не роняют тик — остальные серверы обрабатываются.
func (c *TrafficCron) tick(ctx context.Context) {
	start := time.Now()

	// 1) Резолвим email → vpn_user_id один раз за тик. Для 132 юзеров
	//    <1ms. Если база подрастёт — map+lookup всё ещё O(1) на запись.
	emailToID, err := c.repo.ListVPNUserIDByEmail(ctx)
	if err != nil {
		c.logger.Error("traffic cron: list emails failed", zap.Error(err))
		return
	}
	if len(emailToID) == 0 {
		return // пока нет ни одного vpn_user — ничего не собираем
	}

	// 2) Перечисляем активные серверы. ListServers читает флаг is_active.
	servers, err := c.repo.ListServers(ctx, true)
	if err != nil {
		c.logger.Error("traffic cron: list servers failed", zap.Error(err))
		return
	}
	if len(servers) == 0 {
		return
	}

	// 3) По каждому серверу — один bulk QueryStats(reset=true).
	//    Парсим результат, собираем в общий batch сэмплов.
	//    CollectedAt фиксируем ОДНИМ значением для всей пачки — это
	//    момент окончания тика. В разных серверах тики могут разъехаться
	//    на секунды, но для 5-минутного разрешения неважно.
	collectedAt := time.Now()
	var batch []repository.TrafficSampleInput
	var serverStats []serverTickStat

	for _, srv := range servers {
		// Lazy-connect, как и в Heartbeat'е (pool.GetOrConnect).
		cli := c.pool.Get(srv.ID)
		if cli == nil {
			addr := fmt.Sprintf("%s:%d", srv.XrayAPIHost, srv.XrayAPIPort)
			cli, err = c.pool.GetOrConnect(ctx, srv.ID, addr)
			if err != nil {
				c.logger.Debug("traffic cron: server unreachable, skip",
					zap.Int32("server_id", srv.ID),
					zap.String("addr", addr),
					zap.Error(err),
				)
				continue
			}
		}

		stats, err := cli.QueryAllUserStats(ctx, true) // reset=true!
		if err != nil {
			c.logger.Warn("traffic cron: query stats failed",
				zap.Int32("server_id", srv.ID),
				zap.String("name", srv.Name),
				zap.Error(err))
			continue
		}

		added := 0
		skipped := 0
		for email, s := range stats {
			if s.Uplink == 0 && s.Downlink == 0 {
				continue
			}
			vpnUserID, ok := emailToID[email]
			if !ok {
				// xray помнит email для юзера, которого удалили из нашей БД
				// но не удалили из xray (рассинхрон). Не блокирует, но в
				// debug-лог чтобы заметить.
				c.logger.Debug("traffic cron: orphan email in xray",
					zap.Int32("server_id", srv.ID),
					zap.String("email", email))
				skipped++
				continue
			}
			batch = append(batch, repository.TrafficSampleInput{
				VPNUserID:     vpnUserID,
				ServerID:      srv.ID,
				UplinkBytes:   s.Uplink,
				DownlinkBytes: s.Downlink,
			})
			added++
		}

		serverStats = append(serverStats, serverTickStat{
			serverID: srv.ID,
			name:     srv.Name,
			added:    added,
			orphans:  skipped,
		})
	}

	// 4) Батч-INSERT + транзакционный UPDATE users.
	if len(batch) == 0 {
		c.logger.Debug("traffic cron: no traffic in tick",
			zap.Int("servers_checked", len(servers)),
			zap.Duration("elapsed", time.Since(start)),
		)
		return
	}

	if err := c.repo.InsertTrafficSamplesBatch(ctx, batch, collectedAt); err != nil {
		c.logger.Error("traffic cron: insert batch failed",
			zap.Int("batch_size", len(batch)),
			zap.Error(err),
		)
		return
	}

	c.logger.Info("traffic cron tick",
		zap.Int("samples_written", len(batch)),
		zap.Int("servers_checked", len(servers)),
		zap.Any("per_server", serverStats),
		zap.Duration("elapsed", time.Since(start)),
	)
}

type serverTickStat struct {
	serverID int32
	name     string
	added    int
	orphans  int
}

// ParseVPNUserIDFromEmail — helper: "user123@vpn.local" → 123. Не
// используется внутри TrafficCron (там резолвим через map из БД), но
// может пригодиться в тестах/утилитах. Оставлен как public API.
func ParseVPNUserIDFromEmail(email string) (int64, error) {
	if !strings.HasPrefix(email, userEmailPrefix) || !strings.HasSuffix(email, userEmailSuffix) {
		return 0, fmt.Errorf("unexpected email format: %q", email)
	}
	idStr := email[len(userEmailPrefix) : len(email)-len(userEmailSuffix)]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse id from %q: %w", email, err)
	}
	return id, nil
}
