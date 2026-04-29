package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vpn/vpn-service/internal/model"
)

type VPNRepository struct {
	db *pgxpool.Pool
}

func NewVPNRepository(db *pgxpool.Pool) *VPNRepository {
	return &VPNRepository{db: db}
}

// Servers
func (r *VPNRepository) ListServers(ctx context.Context, activeOnly bool) ([]*model.VPNServer, error) {
	query := `SELECT id, name, location, country_code, host, port, public_key, short_id, dest,
		server_names, xray_api_host, xray_api_port, inbound_tag, is_active, load_percent,
		server_max_connections, description, created_at
		FROM vpn_servers`
	if activeOnly {
		query += ` WHERE is_active = true`
	}
	query += ` ORDER BY load_percent, name`

	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list servers: %w", err)
	}
	defer rows.Close()

	var servers []*model.VPNServer
	for rows.Next() {
		server := &model.VPNServer{}
		if err := rows.Scan(&server.ID, &server.Name, &server.Location, &server.CountryCode, &server.Host, &server.Port,
			&server.PublicKey, &server.ShortID, &server.Dest, &server.ServerNames,
			&server.XrayAPIHost, &server.XrayAPIPort, &server.InboundTag,
			&server.IsActive, &server.LoadPercent, &server.ServerMaxConnections, &server.Description,
			&server.CreatedAt); err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}

	return servers, nil
}

func (r *VPNRepository) GetServer(ctx context.Context, serverID int32) (*model.VPNServer, error) {
	query := `SELECT id, name, location, country_code, host, port, public_key, private_key, short_id, dest,
		server_names, xray_api_host, xray_api_port, inbound_tag, is_active, load_percent,
		server_max_connections, description, created_at FROM vpn_servers WHERE id = $1`

	server := &model.VPNServer{}
	err := r.db.QueryRow(ctx, query, serverID).Scan(
		&server.ID, &server.Name, &server.Location, &server.CountryCode, &server.Host, &server.Port,
		&server.PublicKey, &server.PrivateKey, &server.ShortID, &server.Dest, &server.ServerNames,
		&server.XrayAPIHost, &server.XrayAPIPort, &server.InboundTag, &server.IsActive, &server.LoadPercent,
		&server.ServerMaxConnections, &server.Description, &server.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get server: %w", err)
	}

	return server, nil
}

// UpsertServerByHostPort — идемпотентный seed. Используется VPN Service при
// старте, чтобы занести локальный Xray-сервер из env в БД или обновить
// crypto-поля (когда ключи ротируются).
//
// Identity — пара (host, port), физический Xray-инбаунд. Это позволяет админу
// свободно переименовывать сервер (name, location, country_code) — seed не
// перетирает эти поля на UPDATE. Ранее identity был по name; после первого
// же переименования сидер INSERT-ил дубликат.
//
// Что обновляется при конфликте:
//   - crypto: public_key, private_key, short_id (нужно при ротации Reality-ключей)
//   - Xray wiring: dest, server_names, xray_api_host/port, inbound_tag
//
// Что НЕ трогается (владение админа):
//   - name, location, country_code (дисплейные)
//   - is_active (админ выключает/включает сервер)
//   - server_max_connections, description, load_percent
func (r *VPNRepository) UpsertServerByHostPort(ctx context.Context, s *model.VPNServer) (*model.VPNServer, error) {
	query := `
		INSERT INTO vpn_servers (
			name, location, country_code, host, port,
			public_key, private_key, short_id, dest, server_names,
			xray_api_host, xray_api_port, inbound_tag, is_active
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14
		)
		ON CONFLICT (host, port) DO UPDATE SET
			public_key    = EXCLUDED.public_key,
			private_key   = EXCLUDED.private_key,
			short_id      = EXCLUDED.short_id,
			dest          = EXCLUDED.dest,
			server_names  = EXCLUDED.server_names,
			xray_api_host = EXCLUDED.xray_api_host,
			xray_api_port = EXCLUDED.xray_api_port,
			inbound_tag   = EXCLUDED.inbound_tag
		RETURNING id, name, location, country_code, is_active,
		          server_max_connections, description, created_at
	`

	err := r.db.QueryRow(ctx, query,
		s.Name, s.Location, s.CountryCode, s.Host, s.Port,
		s.PublicKey, s.PrivateKey, s.ShortID, s.Dest, s.ServerNames,
		s.XrayAPIHost, s.XrayAPIPort, s.InboundTag, s.IsActive,
	).Scan(
		&s.ID, &s.Name, &s.Location, &s.CountryCode, &s.IsActive,
		&s.ServerMaxConnections, &s.Description, &s.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to upsert server: %w", err)
	}
	return s, nil
}

// UpdateServerLoad пересчитывает load_percent для одного сервера.
// Формула: load_percent = COUNT(active_connections WHERE server_id AND last_seen > NOW()-5min) * 100 / server_max_connections
func (r *VPNRepository) UpdateServerLoad(ctx context.Context, serverID int32) error {
	const q = `
		UPDATE vpn_servers vs
		SET load_percent = LEAST(100, GREATEST(0, (
			SELECT COALESCE(COUNT(*) * 100 / NULLIF(vs.server_max_connections, 0), 0)::int
			FROM active_connections ac
			WHERE ac.server_id = vs.id
			  AND ac.last_seen > NOW() - INTERVAL '5 minutes'
		)))
		WHERE vs.id = $1
	`
	_, err := r.db.Exec(ctx, q, serverID)
	return err
}

// ListServerIDs возвращает только id-шки — дёшево для cron-ного цикла.
func (r *VPNRepository) ListActiveServerIDs(ctx context.Context) ([]int32, error) {
	rows, err := r.db.Query(ctx, `SELECT id FROM vpn_servers WHERE is_active = true`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// VPN Users
//
// CreateVPNUser — идемпотентная вставка через ON CONFLICT DO NOTHING.
// Возвращает (user, created), где created=true если строка была реально
// создана, false если уже существовала (пара (user_id, subscription_id) — UNIQUE).
//
// При created=false возвращается **существующая** запись с её UUID/токеном —
// это важно: caller обязан использовать тот же UUID для Xray.AddUser, иначе
// зарегистрирует "новый" inbound user, не совпадающий с тем, что в БД.
//
// Зачем так:
//   - повторные webhook'и от Platega (32-часовой ретрай) не должны создавать
//     дубликаты или валиться с 23505;
//   - триал → платная подписка через upsert subscriptions переиспользует
//     subscription_id, поэтому vpn_users тоже остаётся прежний;
//   - chargeback → ре-активация подписки тоже работает идемпотентно.
func (r *VPNRepository) CreateVPNUser(ctx context.Context, userID, subscriptionID int64, uuid, email, flow, subscriptionToken string) (vpnUser *model.VPNUser, created bool, err error) {
	const insertQ = `
		INSERT INTO vpn_users (user_id, subscription_id, uuid, email, flow, subscription_token)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id, subscription_id) DO NOTHING
		RETURNING id, user_id, subscription_id, uuid, email, flow, subscription_token, created_at
	`

	vpnUser = &model.VPNUser{}
	err = r.db.QueryRow(ctx, insertQ, userID, subscriptionID, uuid, email, flow, subscriptionToken).Scan(
		&vpnUser.ID, &vpnUser.UserID, &vpnUser.SubscriptionID, &vpnUser.UUID,
		&vpnUser.Email, &vpnUser.Flow, &vpnUser.SubscriptionToken, &vpnUser.CreatedAt,
	)
	if err == nil {
		return vpnUser, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("failed to create vpn user: %w", err)
	}

	// ON CONFLICT сработал — берём существующую запись.
	const selectQ = `
		SELECT id, user_id, subscription_id, uuid, email, flow, subscription_token, created_at
		FROM vpn_users
		WHERE user_id = $1 AND subscription_id = $2
	`
	if err := r.db.QueryRow(ctx, selectQ, userID, subscriptionID).Scan(
		&vpnUser.ID, &vpnUser.UserID, &vpnUser.SubscriptionID, &vpnUser.UUID,
		&vpnUser.Email, &vpnUser.Flow, &vpnUser.SubscriptionToken, &vpnUser.CreatedAt,
	); err != nil {
		return nil, false, fmt.Errorf("conflict but row not found: %w", err)
	}
	return vpnUser, false, nil
}

func (r *VPNRepository) GetVPNUserByUserID(ctx context.Context, userID int64) (*model.VPNUser, error) {
	query := `SELECT id, user_id, subscription_id, uuid, email, flow, subscription_token, created_at
		FROM vpn_users WHERE user_id = $1 ORDER BY created_at DESC LIMIT 1`

	vpnUser := &model.VPNUser{}
	err := r.db.QueryRow(ctx, query, userID).Scan(
		&vpnUser.ID, &vpnUser.UserID, &vpnUser.SubscriptionID, &vpnUser.UUID,
		&vpnUser.Email, &vpnUser.Flow, &vpnUser.SubscriptionToken, &vpnUser.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("vpn user not found: %w", err)
	}

	return vpnUser, nil
}

// GetSubscriptionConfigByToken — единый JOIN vpn_users × subscriptions
// по subscription_token. Возвращает vpn_user + expires_at/max_devices
// активной подписки. Если токен не найден или подписка неактивна/истекла —
// error. Именно точка входа для публичного subscription endpoint'а.
//
// Статусы считаются активными: 'active', 'trial'.
func (r *VPNRepository) GetSubscriptionConfigByToken(ctx context.Context, token string) (*model.VPNUser, time.Time, int32, error) {
	query := `
		SELECT vu.id, vu.user_id, vu.subscription_id, vu.uuid, vu.email, vu.flow,
		       vu.subscription_token, vu.created_at,
		       s.expires_at, s.max_devices
		FROM vpn_users vu
		JOIN subscriptions s ON s.id = vu.subscription_id
		WHERE vu.subscription_token = $1
		  AND s.status IN ('active', 'trial')
		  AND s.expires_at > NOW()
	`
	vpnUser := &model.VPNUser{}
	var expiresAt time.Time
	var maxDevices int32
	err := r.db.QueryRow(ctx, query, token).Scan(
		&vpnUser.ID, &vpnUser.UserID, &vpnUser.SubscriptionID, &vpnUser.UUID,
		&vpnUser.Email, &vpnUser.Flow, &vpnUser.SubscriptionToken, &vpnUser.CreatedAt,
		&expiresAt, &maxDevices,
	)
	if err != nil {
		return nil, time.Time{}, 0, fmt.Errorf("subscription by token: %w", err)
	}
	return vpnUser, expiresAt, maxDevices, nil
}

// ListAllVPNUsers возвращает id+email всех юзеров — используется heartbeat-ом
// для опроса Xray Stats API.
func (r *VPNRepository) ListAllVPNUsers(ctx context.Context) ([]*model.VPNUser, error) {
	query := `SELECT id, user_id, subscription_id, uuid, email, flow, subscription_token, created_at FROM vpn_users`
	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list vpn users: %w", err)
	}
	defer rows.Close()

	var users []*model.VPNUser
	for rows.Next() {
		u := &model.VPNUser{}
		if err := rows.Scan(&u.ID, &u.UserID, &u.SubscriptionID, &u.UUID, &u.Email, &u.Flow, &u.SubscriptionToken, &u.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, nil
}

// GetSubscriptionMaxDevices возвращает max_devices активной подписки vpn-юзера.
// Кросс-сервисный JOIN — читаем из таблицы subscription-service (та же БД,
// public schema). Триал считается активной подпиской (status='trial').
// Если подписка не активна / истекла — возвращаем (0, pgx.ErrNoRows).
func (r *VPNRepository) GetSubscriptionMaxDevices(ctx context.Context, vpnUserID int64) (int32, error) {
	query := `
		SELECT s.max_devices
		FROM vpn_users vu
		JOIN subscriptions s ON s.id = vu.subscription_id
		WHERE vu.id = $1 AND s.status IN ('active', 'trial') AND s.expires_at > NOW()
	`
	var maxDevices int32
	err := r.db.QueryRow(ctx, query, vpnUserID).Scan(&maxDevices)
	if err != nil {
		return 0, fmt.Errorf("get max_devices for vpn_user %d: %w", vpnUserID, err)
	}
	return maxDevices, nil
}

// Active Connections

// CountActiveDevices — сколько устройств юзера с last_seen ещё свежий.
// window — насколько давно должно быть last_seen чтобы считать устройство живым.
func (r *VPNRepository) CountActiveDevices(ctx context.Context, vpnUserID int64, window time.Duration) (int32, error) {
	query := `
		SELECT COUNT(*) FROM active_connections
		WHERE vpn_user_id = $1 AND last_seen > NOW() - ($2::text)::interval
	`
	var count int32
	// передаём window как текст '300 seconds' — pgx понимает interval из строки
	interval := fmt.Sprintf("%d seconds", int(window.Seconds()))
	err := r.db.QueryRow(ctx, query, vpnUserID, interval).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active devices: %w", err)
	}
	return count, nil
}

// UpsertActiveConnection — создать или обновить запись про устройстве.
// Уникальный ключ: (vpn_user_id, device_identifier). При обновлении
// бьётся last_seen=NOW(), server_id меняется на актуальный.
//
// Используется legacy-путём GetVLESSLink (per-server). Для subscription-
// fetch'а (без конкретного сервера) есть UpsertDeviceTouch ниже.
func (r *VPNRepository) UpsertActiveConnection(ctx context.Context, vpnUserID int64, serverID int32, deviceIdentifier string) (*model.ActiveConnection, error) {
	query := `
		INSERT INTO active_connections (vpn_user_id, server_id, device_identifier, connected_at, last_seen)
		VALUES ($1, $2, $3, NOW(), NOW())
		ON CONFLICT (vpn_user_id, device_identifier) DO UPDATE SET
			server_id = EXCLUDED.server_id,
			last_seen = NOW()
		RETURNING id, vpn_user_id, COALESCE(server_id, 0), device_identifier, connected_at, last_seen
	`
	conn := &model.ActiveConnection{}
	err := r.db.QueryRow(ctx, query, vpnUserID, serverID, deviceIdentifier).Scan(
		&conn.ID, &conn.VPNUserID, &conn.ServerID, &conn.DeviceIdentifier, &conn.ConnectedAt, &conn.LastSeen,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert active connection: %w", err)
	}
	return conn, nil
}

// UpsertDeviceTouch — UPSERT без server_id (server_id IS NULL). Вызывается
// gateway'ем при запросе клиентом subscription URL: device_identifier =
// нормализованный User-Agent. Если строка уже была (с server_id привязанным
// через GetVLESSLink) — server_id НЕ затирается, обновляется только last_seen.
//
// Возвращает строку и флаг created (true если только что создана).
func (r *VPNRepository) UpsertDeviceTouch(ctx context.Context, vpnUserID int64, deviceIdentifier string) (*model.ActiveConnection, bool, error) {
	query := `
		INSERT INTO active_connections (vpn_user_id, server_id, device_identifier, connected_at, last_seen)
		VALUES ($1, NULL, $2, NOW(), NOW())
		ON CONFLICT (vpn_user_id, device_identifier) DO UPDATE SET
			last_seen = NOW()
		RETURNING id, vpn_user_id, COALESCE(server_id, 0), device_identifier, connected_at, last_seen,
		          (xmax = 0) AS inserted
	`
	conn := &model.ActiveConnection{}
	var inserted bool
	err := r.db.QueryRow(ctx, query, vpnUserID, deviceIdentifier).Scan(
		&conn.ID, &conn.VPNUserID, &conn.ServerID, &conn.DeviceIdentifier, &conn.ConnectedAt, &conn.LastSeen,
		&inserted,
	)
	if err != nil {
		return nil, false, fmt.Errorf("upsert device touch: %w", err)
	}
	return conn, inserted, nil
}

// UpdateLastSeenByVPNUser — обновляет last_seen=NOW() для ВСЕХ устройств
// юзера. Вызывается heartbeat-ом когда Xray показал рост трафика.
// См. ограничение модели в docs/services/device-limit.md.
func (r *VPNRepository) UpdateLastSeenByVPNUser(ctx context.Context, vpnUserID int64) error {
	_, err := r.db.Exec(ctx, `UPDATE active_connections SET last_seen = NOW() WHERE vpn_user_id = $1`, vpnUserID)
	return err
}

// Active Connections.
// COALESCE(server_id, 0) — для записей из subscription-touch'а server_id IS NULL.
// API-слой трактует 0 как "сервер не привязан" и не пытается резолвить имя.
func (r *VPNRepository) GetActiveConnections(ctx context.Context, vpnUserID int64) ([]*model.ActiveConnection, error) {
	query := `
		SELECT ac.id, ac.vpn_user_id, COALESCE(ac.server_id, 0), ac.device_identifier, ac.connected_at, ac.last_seen
		FROM active_connections ac
		WHERE ac.vpn_user_id = $1
		ORDER BY ac.last_seen DESC
	`

	rows, err := r.db.Query(ctx, query, vpnUserID)
	if err != nil {
		return nil, fmt.Errorf("failed to get active connections: %w", err)
	}
	defer rows.Close()

	var connections []*model.ActiveConnection
	for rows.Next() {
		conn := &model.ActiveConnection{}
		if err := rows.Scan(&conn.ID, &conn.VPNUserID, &conn.ServerID, &conn.DeviceIdentifier, &conn.ConnectedAt, &conn.LastSeen); err != nil {
			return nil, err
		}
		connections = append(connections, conn)
	}

	return connections, nil
}

func (r *VPNRepository) DisconnectDevice(ctx context.Context, connectionID int64) error {
	query := `DELETE FROM active_connections WHERE id = $1`
	_, err := r.db.Exec(ctx, query, connectionID)
	return err
}

// DeleteVPNUser удаляет запись vpn_users (ON DELETE CASCADE чистит
// active_connections). Xray inbound-cleanup делается в сервисе ДО вызова
// этого метода — тут только БД.
func (r *VPNRepository) DeleteVPNUser(ctx context.Context, userID int64) error {
	_, err := r.db.Exec(ctx, `DELETE FROM vpn_users WHERE user_id = $1`, userID)
	return err
}
