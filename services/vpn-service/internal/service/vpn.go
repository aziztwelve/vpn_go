package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/vpn/platform/pkg/xray"
	"github.com/vpn/vpn-service/internal/model"
	"github.com/vpn/vpn-service/internal/repository"
	"go.uber.org/zap"
)

// DeviceActivityWindow — сколько времени без роста трафика устройство
// считается "живым" слотом. По истечении окна slot освобождается, юзер
// может подключить другое устройство.
const DeviceActivityWindow = 5 * time.Minute

// ErrDeviceLimitExceeded — попытка получить VLESS-ссылку для нового устройства
// когда COUNT активных connection'ов уже равен max_devices подписки.
var ErrDeviceLimitExceeded = errors.New("device limit exceeded")

type VPNService struct {
	repo   *repository.VPNRepository
	xray   *xray.Client
	logger *zap.Logger
}

func NewVPNService(repo *repository.VPNRepository, xrayClient *xray.Client, logger *zap.Logger) *VPNService {
	return &VPNService{
		repo:   repo,
		xray:   xrayClient,
		logger: logger,
	}
}

// CreateVPNUser создаёт запись в БД и регистрирует юзера во ВСЕХ активных
// Xray inbound'ах.
//
// Multi-server стратегия: **best-effort partial success**.
//   - Если хоть один сервер принял AddUser → успех. Юзер может пользоваться
//     VPN через этот сервер, а упавшие серверы восстановятся при следующем
//     рестарте/resync.
//   - Если ВСЕ серверы упали → ошибка. Записываем в БД всё равно (для retry
//     cron'а), но возвращаем ошибку клиенту.
//   - "already exists" трактуем как success (идемпотентность retry).
func (s *VPNService) CreateVPNUser(ctx context.Context, userID, subscriptionID int64) (*model.VPNUser, error) {
	userUUID := uuid.New().String()
	email := fmt.Sprintf("user%d@vpn.local", userID)
	flow := "xtls-rprx-vision"

	vpnUser, err := s.repo.CreateVPNUser(ctx, userID, subscriptionID, userUUID, email, flow)
	if err != nil {
		return nil, err
	}

	servers, err := s.repo.ListServers(ctx, true)
	if err != nil {
		s.logger.Error("failed to list active servers for xray registration",
			zap.Int64("user_id", userID), zap.Error(err))
		return nil, fmt.Errorf("list active servers: %w", err)
	}

	var succeeded, failed int
	for _, srv := range servers {
		if err := s.xray.AddUser(ctx, srv.InboundTag, userUUID, email, flow); err != nil {
			if isAlreadyExists(err) {
				s.logger.Warn("xray user already exists, treating as success",
					zap.Int64("user_id", userID),
					zap.Int32("server_id", srv.ID),
				)
				succeeded++
				continue
			}
			failed++
			// Не валим — продолжаем со следующим сервером.
			s.logger.Error("xray AddUser failed — continuing with other servers",
				zap.Int64("user_id", userID),
				zap.Int32("server_id", srv.ID),
				zap.String("inbound_tag", srv.InboundTag),
				zap.Error(err),
			)
			continue
		}
		succeeded++
		s.logger.Info("xray user added",
			zap.Int64("user_id", userID),
			zap.Int32("server_id", srv.ID),
			zap.String("inbound_tag", srv.InboundTag),
		)
	}

	// Если не удалось зарегистрировать ни на одном сервере — ошибка.
	if len(servers) > 0 && succeeded == 0 {
		s.logger.Error("VPN user DB-created but not registered on any Xray server",
			zap.Int64("user_id", userID),
			zap.String("uuid", userUUID),
			zap.Int("failed", failed),
		)
		return vpnUser, fmt.Errorf("failed to register on any of %d Xray servers", len(servers))
	}

	s.logger.Info("VPN user created",
		zap.Int64("user_id", userID),
		zap.String("uuid", userUUID),
		zap.Int("servers_total", len(servers)),
		zap.Int("servers_ok", succeeded),
		zap.Int("servers_failed", failed),
	)
	return vpnUser, nil
}

func (s *VPNService) GetVPNUser(ctx context.Context, userID int64) (*model.VPNUser, error) {
	return s.repo.GetVPNUserByUserID(ctx, userID)
}

// VLESSLinkResult — полный результат выдачи ссылки с состоянием слотов.
type VLESSLinkResult struct {
	Link           string
	Server         *model.VPNServer
	ConnectionID   int64 // 0 если deviceIdentifier пустой
	CurrentDevices int32
	MaxDevices     int32
}

// GenerateVLESSLink возвращает ссылку подключения к серверу serverID.
//
// Если deviceIdentifier != "" — проверяется лимит max_devices подписки:
//   - COUNT активных устройств (last_seen свежее DeviceActivityWindow) < max_devices
//   - Запись в active_connections создаётся/обновляется (UPSERT)
//   - Возвращается ErrDeviceLimitExceeded если лимит достигнут и устройство новое
//
// Если deviceIdentifier == "" — ссылка возвращается без проверок (admin / debug).
func (s *VPNService) GenerateVLESSLink(ctx context.Context, userID int64, serverID int32, deviceIdentifier string) (*VLESSLinkResult, error) {
	vpnUser, err := s.repo.GetVPNUserByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("vpn user not found: %w", err)
	}

	server, err := s.repo.GetServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("server not found: %w", err)
	}

	result := &VLESSLinkResult{Server: server}

	if deviceIdentifier != "" {
		maxDevices, err := s.repo.GetSubscriptionMaxDevices(ctx, vpnUser.ID)
		if err != nil {
			return nil, fmt.Errorf("check subscription: %w", err)
		}
		result.MaxDevices = maxDevices

		// Проверка лимита: существует ли уже connection с этим device_identifier?
		// Если да — это продление (update), лимит не увеличивается.
		// Если нет — это новое устройство, считаем слот.
		existing, err := s.repo.GetActiveConnections(ctx, vpnUser.ID)
		if err != nil {
			return nil, fmt.Errorf("get existing connections: %w", err)
		}
		isNewDevice := true
		for _, c := range existing {
			if c.DeviceIdentifier == deviceIdentifier {
				isNewDevice = false
				break
			}
		}

		if isNewDevice {
			activeCount, err := s.repo.CountActiveDevices(ctx, vpnUser.ID, DeviceActivityWindow)
			if err != nil {
				return nil, fmt.Errorf("count active devices: %w", err)
			}
			if activeCount >= maxDevices {
				s.logger.Info("device limit exceeded",
					zap.Int64("user_id", userID),
					zap.String("device", deviceIdentifier),
					zap.Int32("active", activeCount),
					zap.Int32("max", maxDevices),
				)
				result.CurrentDevices = activeCount
				return result, ErrDeviceLimitExceeded
			}
		}

		// Upsert запись устройства — обновит last_seen даже если было старое.
		conn, err := s.repo.UpsertActiveConnection(ctx, vpnUser.ID, serverID, deviceIdentifier)
		if err != nil {
			return nil, fmt.Errorf("upsert active connection: %w", err)
		}
		result.ConnectionID = conn.ID

		// Пересчитать после upsert'а (для ответа — "2/2")
		result.CurrentDevices, err = s.repo.CountActiveDevices(ctx, vpnUser.ID, DeviceActivityWindow)
		if err != nil {
			return nil, fmt.Errorf("count after upsert: %w", err)
		}
	}

	// Генерация VLESS-ссылки: vless://UUID@HOST:PORT?params#NAME
	vlessURL := fmt.Sprintf("vless://%s@%s:%d", vpnUser.UUID, server.Host, server.Port)
	params := url.Values{}
	params.Add("encryption", "none")
	params.Add("flow", vpnUser.Flow)
	params.Add("security", "reality")
	params.Add("pbk", server.PublicKey)
	params.Add("fp", "chrome")
	params.Add("sni", server.ServerNames)
	params.Add("sid", server.ShortID)
	params.Add("type", "tcp")
	params.Add("headerType", "none")
	result.Link = fmt.Sprintf("%s?%s#%s", vlessURL, params.Encode(), url.QueryEscape(server.Name))

	return result, nil
}

func (s *VPNService) ListServers(ctx context.Context, activeOnly bool) ([]*model.VPNServer, error) {
	return s.repo.ListServers(ctx, activeOnly)
}

func (s *VPNService) GetActiveConnections(ctx context.Context, userID int64) ([]*model.ActiveConnection, int32, error) {
	vpnUser, err := s.repo.GetVPNUserByUserID(ctx, userID)
	if err != nil {
		return nil, 0, fmt.Errorf("vpn user not found: %w", err)
	}

	connections, err := s.repo.GetActiveConnections(ctx, vpnUser.ID)
	if err != nil {
		return nil, 0, err
	}

	return connections, int32(len(connections)), nil
}

// DisconnectDevice удаляет запись active_connection и (пока упрощённо) не
// трогает Xray — запрет подключения по лимиту устройств реализуется через
// атомарный счётчик, а не через физическое удаление VLESS-клиента. Полное
// удаление юзера из Xray-inbound'а происходит через DisableVPNUser (ниже).
func (s *VPNService) DisconnectDevice(ctx context.Context, connectionID int64) error {
	return s.repo.DisconnectDevice(ctx, connectionID)
}

// DisableVPNUser физически удаляет юзера из всех Xray inbound'ов и из БД.
// Вызывается:
//   - payment-service при refund
//   - subscription-service при истечении подписки (cron)
//
// Идемпотентность: если юзера нет в БД — возвращаем (0, nil).
// Ошибки Xray RemoveUser (в т.ч. "not found") игнорируются — считаем успехом.
func (s *VPNService) DisableVPNUser(ctx context.Context, userID int64) (cleaned int32, err error) {
	vpnUser, err := s.repo.GetVPNUserByUserID(ctx, userID)
	if err != nil {
		// Юзер не найден — идемпотентный ответ.
		s.logger.Info("DisableVPNUser: no vpn_user found, nothing to do",
			zap.Int64("user_id", userID))
		return 0, nil
	}

	servers, err := s.repo.ListServers(ctx, true)
	if err != nil {
		return 0, fmt.Errorf("list servers: %w", err)
	}

	for _, srv := range servers {
		if err := s.xray.RemoveUser(ctx, srv.InboundTag, vpnUser.Email); err != nil {
			// "not found" — нормально (например при повторном вызове или при
			// рестарте Xray с потерей in-memory clients). Логируем и идём дальше.
			if isNotFound(err) {
				s.logger.Info("xray RemoveUser: not found (ok)",
					zap.Int64("user_id", userID),
					zap.Int32("server_id", srv.ID),
					zap.String("email", vpnUser.Email),
				)
				cleaned++
				continue
			}
			s.logger.Error("xray RemoveUser failed",
				zap.Int64("user_id", userID),
				zap.Int32("server_id", srv.ID),
				zap.Error(err))
			// Не роняем — продолжаем со следующими серверами.
			continue
		}
		cleaned++
		s.logger.Info("xray user removed",
			zap.Int64("user_id", userID),
			zap.Int32("server_id", srv.ID))
	}

	if err := s.repo.DeleteVPNUser(ctx, userID); err != nil {
		return cleaned, fmt.Errorf("delete vpn_user: %w", err)
	}
	s.logger.Info("VPN user disabled",
		zap.Int64("user_id", userID),
		zap.Int32("servers_cleaned", cleaned))
	return cleaned, nil
}

// ResyncResult — статистика ре-пуша юзеров на новый сервер.
type ResyncResult struct {
	Total        int32
	Added        int32
	AlreadyExist int32
	Failed       int32
}

// ResyncServer — пропушить всех существующих vpn_users в inbound указанного сервера.
// Используется при горизонтальном масштабировании: добавили 3-й VPS → INSERT
// в vpn_servers → ResyncServer(3) → все имеющиеся UUID прописаны в его inbound.
//
// Идемпотентно: "already exists" не ошибка, а expected для retry.
func (s *VPNService) ResyncServer(ctx context.Context, serverID int32) (*ResyncResult, error) {
	srv, err := s.repo.GetServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}

	users, err := s.repo.ListAllVPNUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list vpn users: %w", err)
	}

	res := &ResyncResult{Total: int32(len(users))}
	for _, u := range users {
		if err := s.xray.AddUser(ctx, srv.InboundTag, u.UUID, u.Email, u.Flow); err != nil {
			if isAlreadyExists(err) {
				res.AlreadyExist++
				continue
			}
			res.Failed++
			s.logger.Error("resync: AddUser failed",
				zap.Int32("server_id", serverID),
				zap.Int64("vpn_user_id", u.ID),
				zap.Error(err))
			continue
		}
		res.Added++
	}

	s.logger.Info("ResyncServer done",
		zap.Int32("server_id", serverID),
		zap.Int32("total", res.Total),
		zap.Int32("added", res.Added),
		zap.Int32("already", res.AlreadyExist),
		zap.Int32("failed", res.Failed),
	)
	return res, nil
}

// isNotFound — эвристика Xray: при RemoveUser на несуществующего юзера.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "does not exist")
}

// isAlreadyExists — эвристика: Xray возвращает grpc Internal/Unknown ошибки
// с текстом "already exists" для AddUser на существующего юзера.
func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}
