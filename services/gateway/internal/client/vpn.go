package client

import (
	"context"
	"fmt"

	pb "github.com/vpn/shared/pkg/proto/vpn/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type VPNClient struct {
	client pb.VPNServiceClient
	conn   *grpc.ClientConn
	logger *zap.Logger
}

func NewVPNClient(addr string, logger *zap.Logger) (*VPNClient, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to vpn service: %w", err)
	}

	return &VPNClient{
		client: pb.NewVPNServiceClient(conn),
		conn:   conn,
		logger: logger,
	}, nil
}

func (c *VPNClient) Close() error {
	return c.conn.Close()
}

func (c *VPNClient) CreateVPNUser(ctx context.Context, userID, subscriptionID int64) (*pb.CreateVPNUserResponse, error) {
	return c.client.CreateVPNUser(ctx, &pb.CreateVPNUserRequest{
		UserId:         userID,
		SubscriptionId: subscriptionID,
	})
}

func (c *VPNClient) GetVPNUser(ctx context.Context, userID int64) (*pb.GetVPNUserResponse, error) {
	return c.client.GetVPNUser(ctx, &pb.GetVPNUserRequest{
		UserId: userID,
	})
}

func (c *VPNClient) GetVLESSLink(ctx context.Context, userID int64, serverID int32) (*pb.GetVLESSLinkResponse, error) {
	return c.client.GetVLESSLink(ctx, &pb.GetVLESSLinkRequest{
		UserId:   userID,
		ServerId: serverID,
	})
}

func (c *VPNClient) ListServers(ctx context.Context, activeOnly bool) (*pb.ListServersResponse, error) {
	return c.client.ListServers(ctx, &pb.ListServersRequest{
		ActiveOnly: activeOnly,
	})
}

func (c *VPNClient) GetActiveConnections(ctx context.Context, userID int64) (*pb.GetActiveConnectionsResponse, error) {
	return c.client.GetActiveConnections(ctx, &pb.GetActiveConnectionsRequest{
		UserId: userID,
	})
}
