package client

import (
	"context"
	"fmt"

	pb "github.com/vpn/shared/pkg/proto/auth/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type AuthClient struct {
	client pb.AuthServiceClient
	conn   *grpc.ClientConn
	logger *zap.Logger
}

func NewAuthClient(addr string, logger *zap.Logger) (*AuthClient, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to auth service: %w", err)
	}

	return &AuthClient{
		client: pb.NewAuthServiceClient(conn),
		conn:   conn,
		logger: logger,
	}, nil
}

func (c *AuthClient) Close() error {
	return c.conn.Close()
}

// ValidateTelegramUser — проксирует на auth-service. refToken опционален
// (передаётся фронтом, если в start_param был префикс ref_<token>).
func (c *AuthClient) ValidateTelegramUser(ctx context.Context, initData, refToken string) (*pb.ValidateTelegramUserResponse, error) {
	return c.client.ValidateTelegramUser(ctx, &pb.ValidateTelegramUserRequest{
		InitData: initData,
		RefToken: refToken,
	})
}

func (c *AuthClient) GetUser(ctx context.Context, userID int64) (*pb.GetUserResponse, error) {
	return c.client.GetUser(ctx, &pb.GetUserRequest{
		UserId: userID,
	})
}

func (c *AuthClient) VerifyToken(ctx context.Context, token string) (*pb.VerifyTokenResponse, error) {
	return c.client.VerifyToken(ctx, &pb.VerifyTokenRequest{
		Token: token,
	})
}

// SelfUpdateRole — self-service смена роли (user ↔ partner).
// userID берётся из JWT в gateway-handler'е. Возвращает свежий JWT.
func (c *AuthClient) SelfUpdateRole(ctx context.Context, userID int64, role string) (*pb.SelfUpdateRoleResponse, error) {
	return c.client.SelfUpdateRole(ctx, &pb.SelfUpdateRoleRequest{
		UserId: userID,
		Role:   role,
	})
}
