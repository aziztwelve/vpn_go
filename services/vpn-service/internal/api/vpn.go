package api

import (
	"context"
	"errors"

	"github.com/vpn/vpn-service/internal/model"
	"github.com/vpn/vpn-service/internal/service"
	pb "github.com/vpn/shared/pkg/proto/vpn/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// toPbVPNUser — доменная модель → proto. Вынесено чтобы не дублировать
// маппинг в CreateVPNUser / GetVPNUser / GetSubscriptionConfig.
func toPbVPNUser(u *model.VPNUser) *pb.VPNUser {
	if u == nil {
		return nil
	}
	return &pb.VPNUser{
		Id:                u.ID,
		UserId:            u.UserID,
		SubscriptionId:    u.SubscriptionID,
		Uuid:              u.UUID,
		Email:             u.Email,
		Flow:              u.Flow,
		SubscriptionToken: u.SubscriptionToken,
		CreatedAt:         u.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

type VPNAPI struct {
	pb.UnimplementedVPNServiceServer
	service *service.VPNService
	logger  *zap.Logger
}

func NewVPNAPI(service *service.VPNService, logger *zap.Logger) *VPNAPI {
	return &VPNAPI{
		service: service,
		logger:  logger,
	}
}

func (a *VPNAPI) CreateVPNUser(ctx context.Context, req *pb.CreateVPNUserRequest) (*pb.CreateVPNUserResponse, error) {
	if req.UserId == 0 || req.SubscriptionId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id and subscription_id are required")
	}

	vpnUser, err := a.service.CreateVPNUser(ctx, req.UserId, req.SubscriptionId)
	if err != nil {
		a.logger.Error("Failed to create VPN user", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to create VPN user")
	}

	return &pb.CreateVPNUserResponse{
		VpnUser: toPbVPNUser(vpnUser),
	}, nil
}

func (a *VPNAPI) GetVPNUser(ctx context.Context, req *pb.GetVPNUserRequest) (*pb.GetVPNUserResponse, error) {
	if req.UserId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	vpnUser, err := a.service.GetVPNUser(ctx, req.UserId)
	if err != nil {
		a.logger.Error("Failed to get VPN user", zap.Error(err))
		return nil, status.Error(codes.NotFound, "VPN user not found")
	}

	return &pb.GetVPNUserResponse{
		VpnUser: toPbVPNUser(vpnUser),
	}, nil
}

func (a *VPNAPI) GetVLESSLink(ctx context.Context, req *pb.GetVLESSLinkRequest) (*pb.GetVLESSLinkResponse, error) {
	if req.UserId == 0 || req.ServerId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id and server_id are required")
	}

	res, err := a.service.GenerateVLESSLink(ctx, req.UserId, req.ServerId, req.DeviceIdentifier)
	if errors.Is(err, service.ErrDeviceLimitExceeded) {
		return nil, status.Errorf(codes.ResourceExhausted,
			"device limit exceeded: %d/%d devices active",
			res.CurrentDevices, res.MaxDevices,
		)
	}
	if err != nil {
		a.logger.Error("Failed to generate VLESS link", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to generate VLESS link")
	}

	return &pb.GetVLESSLinkResponse{
		VlessLink: res.Link,
		Server: &pb.Server{
			Id:          res.Server.ID,
			Name:        res.Server.Name,
			Location:    res.Server.Location,
			CountryCode: res.Server.CountryCode,
			Host:        res.Server.Host,
			Port:        res.Server.Port,
			PublicKey:   res.Server.PublicKey,
			ShortId:     res.Server.ShortID,
			Dest:        res.Server.Dest,
			ServerNames: res.Server.ServerNames,
			IsActive:    res.Server.IsActive,
			LoadPercent: res.Server.LoadPercent,
		},
		CurrentDevices: res.CurrentDevices,
		MaxDevices:     res.MaxDevices,
		ConnectionId:   res.ConnectionID,
	}, nil
}

func (a *VPNAPI) ListServers(ctx context.Context, req *pb.ListServersRequest) (*pb.ListServersResponse, error) {
	servers, err := a.service.ListServers(ctx, req.ActiveOnly)
	if err != nil {
		a.logger.Error("Failed to list servers", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to list servers")
	}

	var pbServers []*pb.Server
	for _, server := range servers {
		pbServers = append(pbServers, &pb.Server{
			Id:          server.ID,
			Name:        server.Name,
			Location:    server.Location,
			CountryCode: server.CountryCode,
			Host:        server.Host,
			Port:        server.Port,
			PublicKey:   server.PublicKey,
			ShortId:     server.ShortID,
			Dest:        server.Dest,
			ServerNames: server.ServerNames,
			IsActive:    server.IsActive,
			LoadPercent: server.LoadPercent,
		})
	}

	return &pb.ListServersResponse{Servers: pbServers}, nil
}

func (a *VPNAPI) GetActiveConnections(ctx context.Context, req *pb.GetActiveConnectionsRequest) (*pb.GetActiveConnectionsResponse, error) {
	if req.UserId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	connections, total, err := a.service.GetActiveConnections(ctx, req.UserId)
	if err != nil {
		a.logger.Error("Failed to get active connections", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to get connections")
	}

	var pbConnections []*pb.ActiveConnection
	for _, conn := range connections {
		pbConnections = append(pbConnections, &pb.ActiveConnection{
			Id:               conn.ID,
			VpnUserId:        conn.VPNUserID,
			ServerId:         conn.ServerID,
			DeviceIdentifier: conn.DeviceIdentifier,
			ConnectedAt:      conn.ConnectedAt.Format("2006-01-02T15:04:05Z"),
			LastSeen:         conn.LastSeen.Format("2006-01-02T15:04:05Z"),
		})
	}

	return &pb.GetActiveConnectionsResponse{
		Connections:      pbConnections,
		TotalConnections: total,
	}, nil
}

func (a *VPNAPI) DisconnectDevice(ctx context.Context, req *pb.DisconnectDeviceRequest) (*pb.DisconnectDeviceResponse, error) {
	if req.ConnectionId == 0 {
		return nil, status.Error(codes.InvalidArgument, "connection_id is required")
	}

	err := a.service.DisconnectDevice(ctx, req.ConnectionId)
	if err != nil {
		a.logger.Error("Failed to disconnect device", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to disconnect device")
	}

	return &pb.DisconnectDeviceResponse{Success: true}, nil
}

func (a *VPNAPI) DisableVPNUser(ctx context.Context, req *pb.DisableVPNUserRequest) (*pb.DisableVPNUserResponse, error) {
	if req.GetUserId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}
	cleaned, err := a.service.DisableVPNUser(ctx, req.GetUserId())
	if err != nil {
		a.logger.Error("Failed to disable VPN user", zap.Int64("user_id", req.GetUserId()), zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to disable vpn user")
	}
	return &pb.DisableVPNUserResponse{Success: true, ServersCleaned: cleaned}, nil
}

func (a *VPNAPI) GetSubscriptionConfig(ctx context.Context, req *pb.GetSubscriptionConfigRequest) (*pb.GetSubscriptionConfigResponse, error) {
	if req.GetToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}

	cfg, err := a.service.GetSubscriptionConfig(ctx, req.GetToken())
	if err != nil {
		// Токен не найден или подписка истекла → NotFound.
		a.logger.Info("subscription config lookup failed",
			zap.String("token", req.GetToken()[:min(8, len(req.GetToken()))]+"…"),
			zap.Error(err))
		return nil, status.Error(codes.NotFound, "subscription not found or expired")
	}

	pbServers := make([]*pb.Server, 0, len(cfg.Servers))
	for _, s := range cfg.Servers {
		pbServers = append(pbServers, &pb.Server{
			Id:          s.ID,
			Name:        s.Name,
			Location:    s.Location,
			CountryCode: s.CountryCode,
			Host:        s.Host,
			Port:        s.Port,
			PublicKey:   s.PublicKey,
			ShortId:     s.ShortID,
			Dest:        s.Dest,
			ServerNames: s.ServerNames,
			IsActive:    s.IsActive,
			LoadPercent: s.LoadPercent,
		})
	}

	return &pb.GetSubscriptionConfigResponse{
		VpnUser:    toPbVPNUser(cfg.VPNUser),
		Servers:    pbServers,
		ExpiresAt:  cfg.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
		MaxDevices: cfg.MaxDevices,
	}, nil
}

func (a *VPNAPI) GetSubscriptionToken(ctx context.Context, req *pb.GetSubscriptionTokenRequest) (*pb.GetSubscriptionTokenResponse, error) {
	if req.GetUserId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}
	token, expiresAt, err := a.service.GetSubscriptionToken(ctx, req.GetUserId())
	if err != nil {
		a.logger.Info("subscription token lookup failed", zap.Int64("user_id", req.GetUserId()), zap.Error(err))
		return nil, status.Error(codes.NotFound, "no active subscription for user")
	}
	return &pb.GetSubscriptionTokenResponse{
		SubscriptionToken: token,
		ExpiresAt:         expiresAt.UTC().Format("2006-01-02T15:04:05Z"),
	}, nil
}

func (a *VPNAPI) ResyncServer(ctx context.Context, req *pb.ResyncServerRequest) (*pb.ResyncServerResponse, error) {
	if req.GetServerId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "server_id is required")
	}
	res, err := a.service.ResyncServer(ctx, req.GetServerId())
	if err != nil {
		a.logger.Error("ResyncServer failed", zap.Int32("server_id", req.GetServerId()), zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to resync server")
	}
	return &pb.ResyncServerResponse{
		UsersTotal:   res.Total,
		UsersAdded:   res.Added,
		UsersAlready: res.AlreadyExist,
		UsersFailed:  res.Failed,
	}, nil
}
