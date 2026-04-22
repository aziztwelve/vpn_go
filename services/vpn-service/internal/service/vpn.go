package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/vpn/platform/pkg/xray"
	"github.com/vpn/vpn-service/internal/model"
	"github.com/vpn/vpn-service/internal/repository"
	"go.uber.org/zap"
)

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
// Xray inbound'ах (пока у нас один локальный сервер, но код не предполагает 1).
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

	for _, srv := range servers {
		if err := s.xray.AddUser(ctx, srv.InboundTag, userUUID, email, flow); err != nil {
			// Идемпотентность: если юзер уже есть — логируем и продолжаем.
			if isAlreadyExists(err) {
				s.logger.Warn("xray user already exists, continue",
					zap.Int64("user_id", userID),
					zap.Int32("server_id", srv.ID),
					zap.String("inbound_tag", srv.InboundTag),
				)
				continue
			}
			s.logger.Error("failed to add user to xray",
				zap.Int64("user_id", userID),
				zap.Int32("server_id", srv.ID),
				zap.String("inbound_tag", srv.InboundTag),
				zap.Error(err),
			)
			return nil, fmt.Errorf("xray AddUser on server %d: %w", srv.ID, err)
		}
		s.logger.Info("xray user added",
			zap.Int64("user_id", userID),
			zap.Int32("server_id", srv.ID),
			zap.String("inbound_tag", srv.InboundTag),
		)
	}

	s.logger.Info("VPN user created",
		zap.Int64("user_id", userID),
		zap.String("uuid", userUUID),
		zap.Int("servers_registered", len(servers)),
	)

	return vpnUser, nil
}

func (s *VPNService) GetVPNUser(ctx context.Context, userID int64) (*model.VPNUser, error) {
	return s.repo.GetVPNUserByUserID(ctx, userID)
}

func (s *VPNService) GenerateVLESSLink(ctx context.Context, userID int64, serverID int32) (string, *model.VPNServer, error) {
	vpnUser, err := s.repo.GetVPNUserByUserID(ctx, userID)
	if err != nil {
		return "", nil, fmt.Errorf("vpn user not found: %w", err)
	}

	server, err := s.repo.GetServer(ctx, serverID)
	if err != nil {
		return "", nil, fmt.Errorf("server not found: %w", err)
	}

	// Format: vless://UUID@HOST:PORT?params#NAME
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

	vlessLink := fmt.Sprintf("%s?%s#%s", vlessURL, params.Encode(), url.QueryEscape(server.Name))
	return vlessLink, server, nil
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
// удаление юзера из Xray-inbound'а происходит на уровне отдельного метода
// DeleteVPNUser (ниже).
func (s *VPNService) DisconnectDevice(ctx context.Context, connectionID int64) error {
	return s.repo.DisconnectDevice(ctx, connectionID)
}

// isAlreadyExists — эвристика: Xray возвращает grpc Internal/Unknown ошибки
// с текстом "already exists" для AddUser на существующего юзера.
func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}
