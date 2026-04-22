package api

import (
	"context"
	"errors"

	"github.com/vpn/vpn-service/internal/service"
	pb "github.com/vpn/shared/pkg/proto/vpn/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
		VpnUser: &pb.VPNUser{
			Id:             vpnUser.ID,
			UserId:         vpnUser.UserID,
			SubscriptionId: vpnUser.SubscriptionID,
			Uuid:           vpnUser.UUID,
			Email:          vpnUser.Email,
			Flow:           vpnUser.Flow,
			CreatedAt:      vpnUser.CreatedAt.Format("2006-01-02T15:04:05Z"),
		},
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
		VpnUser: &pb.VPNUser{
			Id:             vpnUser.ID,
			UserId:         vpnUser.UserID,
			SubscriptionId: vpnUser.SubscriptionID,
			Uuid:           vpnUser.UUID,
			Email:          vpnUser.Email,
			Flow:           vpnUser.Flow,
			CreatedAt:      vpnUser.CreatedAt.Format("2006-01-02T15:04:05Z"),
		},
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
