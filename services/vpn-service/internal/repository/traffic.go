package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// TrafficSampleInput — одна строка для вставки в traffic_samples.
type TrafficSampleInput struct {
	VPNUserID     int64
	ServerID      int32
	UplinkBytes   int64
	DownlinkBytes int64
}

// InsertTrafficSamplesBatch вставляет пачку ненулевых сэмплов одной
// транзакцией + в той же транзакции обновляет users.first_connection_at
// (COALESCE — ставится один раз) и users.last_traffic_at (каждый раз).
//
// vpn_users.user_id используется как мост к users.id:
//   UPDATE users SET ... WHERE id = (SELECT user_id FROM vpn_users WHERE id = $X)
//
// collectedAt фиксируется единым значением для всей пачки — это момент
// окончания тика TrafficCron, гарантирует что вся пачка имеет одинаковый
// timestamp и запросы «трафик за последние 5 минут» не пропустят границу.
//
// При пустом samples — no-op (не открываем транзакцию).
func (r *VPNRepository) InsertTrafficSamplesBatch(
	ctx context.Context,
	samples []TrafficSampleInput,
	collectedAt time.Time,
) error {
	if len(samples) == 0 {
		return nil
	}

	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx) // no-op если уже закоммичено
	}()

	// 1) INSERT в traffic_samples. pgx.CopyFrom даёт N-кратное ускорение
	//    на больших пачках; для <1000 строк за тик INSERT VALUES тоже
	//    нормально, но CopyFrom проще и выживёт рост.
	rows := make([][]any, len(samples))
	for i, s := range samples {
		rows[i] = []any{s.VPNUserID, s.ServerID, s.UplinkBytes, s.DownlinkBytes, collectedAt}
	}
	_, err = tx.CopyFrom(
		ctx,
		pgx.Identifier{"traffic_samples"},
		[]string{"vpn_user_id", "server_id", "uplink_bytes", "downlink_bytes", "collected_at"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("copy into traffic_samples: %w", err)
	}

	// 2) Дедуплицируем по vpn_user_id — в одном тике юзер может быть
	//    на нескольких серверах, но в users UPDATE делаем один раз.
	uniqueUsers := make(map[int64]struct{}, len(samples))
	for _, s := range samples {
		uniqueUsers[s.VPNUserID] = struct{}{}
	}
	vpnUserIDs := make([]int64, 0, len(uniqueUsers))
	for id := range uniqueUsers {
		vpnUserIDs = append(vpnUserIDs, id)
	}

	// 3) Один запрос, обновляющий все затронутые users в одной транзакции.
	//    COALESCE для first_connection_at — ставим только если был NULL.
	//    last_traffic_at — всегда перезаписываем на collectedAt.
	_, err = tx.Exec(ctx, `
		UPDATE users u
		SET first_connection_at = COALESCE(u.first_connection_at, $1),
		    last_traffic_at = $1
		FROM vpn_users vu
		WHERE vu.user_id = u.id
		  AND vu.id = ANY($2::bigint[])
	`, collectedAt, vpnUserIDs)
	if err != nil {
		return fmt.Errorf("update users activity denorm: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// SumTrafficSince возвращает сумму uplink+downlink для юзера с момента since.
// Для больших периодов (>90 дней = cleanup-cutoff) вернёт неполный — но мы
// retention-окнами ограничиваемся сутками-неделями.
func (r *VPNRepository) SumTrafficSince(ctx context.Context, vpnUserID int64, since time.Time) (int64, error) {
	var total int64
	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(uplink_bytes + downlink_bytes), 0)
		FROM traffic_samples
		WHERE vpn_user_id = $1 AND collected_at >= $2
	`, vpnUserID, since).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum traffic since: %w", err)
	}
	return total, nil
}

// DeleteTrafficSamplesOlderThan удаляет сэмплы старше cutoff. Вызывается
// daily cleanup cron'ом (см. service/cleanup_cron.go).
func (r *VPNRepository) DeleteTrafficSamplesOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM traffic_samples WHERE collected_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete old traffic samples: %w", err)
	}
	return tag.RowsAffected(), nil
}

// HasTrafficSamplesForServer проверяет, есть ли в traffic_samples хотя бы
// одна строка для данного server_id. Используется TrafficCron'ом чтобы
// отличить «первый тик для этого сервера за всю историю сервиса» (когда
// накопленный в xray baseline надо отбросить, иначе он запишется как одна
// гигантская дельта за 5-минутное окно) от обычного тика.
//
// Важно: после рестарта сервиса traffic_samples уже содержит записи, и
// этот метод вернёт true — значит дельта между тиками (включая тики до
// рестарта) будет засчитана честно, потеряется только окно простоя.
func (r *VPNRepository) HasTrafficSamplesForServer(ctx context.Context, serverID int32) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM traffic_samples WHERE server_id = $1)
	`, serverID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check traffic samples for server: %w", err)
	}
	return exists, nil
}

// ListVPNUserIDByEmail — map email → vpn_user_id для всех записей в vpn_users.
// Используется TrafficCron'ом для резолвинга "user{ID}@vpn.local" из xray
// stats в нашу внутреннюю id-шку. Email в xray = email в vpn_users.
func (r *VPNRepository) ListVPNUserIDByEmail(ctx context.Context) (map[string]int64, error) {
	rows, err := r.db.Query(ctx, `SELECT email, id FROM vpn_users`)
	if err != nil {
		return nil, fmt.Errorf("list vpn users email→id: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int64, 256)
	for rows.Next() {
		var email string
		var id int64
		if err := rows.Scan(&email, &id); err != nil {
			return nil, err
		}
		out[email] = id
	}
	return out, nil
}


