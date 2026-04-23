package repository

import (
	"context"
	"fmt"
	"time"

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
		server_names, inbound_tag, is_active, load_percent, server_max_connections, description, created_at
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
			&server.PublicKey, &server.ShortID, &server.Dest, &server.ServerNames, &server.InboundTag,
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
func (r *VPNRepository) CreateVPNUser(ctx context.Context, userID, subscriptionID int64, uuid, email, flow string) (*model.VPNUser, error) {
	query := `
		INSERT INTO vpn_users (user_id, subscription_id, uuid, email, flow)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, user_id, subscription_id, uuid, email, flow, created_at
	`

	vpnUser := &model.VPNUser{}
	err := r.db.QueryRow(ctx, query, userID, subscriptionID, uuid, email, flow).Scan(
		&vpnUser.ID, &vpnUser.UserID, &vpnUser.SubscriptionID, &vpnUser.UUID, &vpnUser.Email, &vpnUser.Flow, &vpnUser.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create vpn user: %w", err)
	}

	return vpnUser, nil
}

func (r *VPNRepository) GetVPNUserByUserID(ctx context.Context, userID int64) (*model.VPNUser, error) {
	query := `SELECT id, user_id, subscription_id, uuid, email, flow, created_at FROM vpn_users WHERE user_id = $1 ORDER BY created_at DESC LIMIT 1`

	vpnUser := &model.VPNUser{}
	err := r.db.QueryRow(ctx, query, userID).Scan(
		&vpnUser.ID, &vpnUser.UserID, &vpnUser.SubscriptionID, &vpnUser.UUID, &vpnUser.Email, &vpnUser.Flow, &vpnUser.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("vpn user not found: %w", err)
	}

	return vpnUser, nil
}

// ListAllVPNUsers возвращает id+email всех юзеров — используется heartbeat-ом
// для опроса Xray Stats API.
func (r *VPNRepository) ListAllVPNUsers(ctx context.Context) ([]*model.VPNUser, error) {
	query := `SELECT id, user_id, subscription_id, uuid, email, flow, created_at FROM vpn_users`
	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list vpn users: %w", err)
	}
	defer rows.Close()

	var users []*model.VPNUser
	for rows.Next() {
		u := &model.VPNUser{}
		if err := rows.Scan(&u.ID, &u.UserID, &u.SubscriptionID, &u.UUID, &u.Email, &u.Flow, &u.CreatedAt); err != nil {
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

// UpsertActiveConnection — создать или обновить запись about устройстве.
// Уникальный ключ: (vpn_user_id, device_identifier). При обновлении
// бьётся last_seen=NOW(), server_id меняется на актуальный.
func (r *VPNRepository) UpsertActiveConnection(ctx context.Context, vpnUserID int64, serverID int32, deviceIdentifier string) (*model.ActiveConnection, error) {
	query := `
		INSERT INTO active_connections (vpn_user_id, server_id, device_identifier, connected_at, last_seen)
		VALUES ($1, $2, $3, NOW(), NOW())
		ON CONFLICT (vpn_user_id, device_identifier) DO UPDATE SET
			server_id = EXCLUDED.server_id,
			last_seen = NOW()
		RETURNING id, vpn_user_id, server_id, device_identifier, connected_at, last_seen
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

// UpdateLastSeenByVPNUser — обновляет last_seen=NOW() для ВСЕХ устройств
// юзера. Вызывается heartbeat-ом когда Xray показал рост трафика.
// См. ограничение модели в docs/services/device-limit.md.
func (r *VPNRepository) UpdateLastSeenByVPNUser(ctx context.Context, vpnUserID int64) error {
	_, err := r.db.Exec(ctx, `UPDATE active_connections SET last_seen = NOW() WHERE vpn_user_id = $1`, vpnUserID)
	return err
}

// Active Connections
func (r *VPNRepository) GetActiveConnections(ctx context.Context, vpnUserID int64) ([]*model.ActiveConnection, error) {
	query := `
		SELECT ac.id, ac.vpn_user_id, ac.server_id, ac.device_identifier, ac.connected_at, ac.last_seen
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
