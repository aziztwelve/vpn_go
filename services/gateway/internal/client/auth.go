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

// Conn returns the underlying gRPC connection (for health checks).
func (c *AuthClient) Conn() *grpc.ClientConn { return c.conn }

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

// SetPendingReferral — сохранить ref_token по telegram_id ДО регистрации
// (вызывается из webhook'а бота при /start ref_<token>).
func (c *AuthClient) SetPendingReferral(ctx context.Context, telegramID int64, refToken string) error {
	_, err := c.client.SetPendingReferral(ctx, &pb.SetPendingReferralRequest{
		TelegramId: telegramID,
		RefToken:   refToken,
	})
	return err
}

// RecordBotStart — фиксирует первое нажатие /start (для воронки бот → Mini App).
// Best-effort: вызывается из webhook'а бота, ошибка не блокирует ответ юзеру.
//
// Возвращает (stored, campaignID, err):
//   - stored=true только при первом /start этого telegram_id
//   - campaignID>0 если start_param = "src_<slug>" и кампания активна
func (c *AuthClient) RecordBotStart(ctx context.Context, telegramID int64, username, firstName, startParam string) (bool, int64, error) {
	resp, err := c.client.RecordBotStart(ctx, &pb.RecordBotStartRequest{
		TelegramId: telegramID,
		Username:   username,
		FirstName:  firstName,
		StartParam: startParam,
	})
	if err != nil {
		return false, 0, err
	}
	return resp.Stored, resp.CampaignId, nil
}

// SetPendingCampaign — сохранить campaign-атрибуцию по telegram_id ДО
// регистрации в Mini App (вызывается из webhook'а бота при /start src_<slug>).
// Возвращает campaignID=0 если slug не найден/архивирован (не ошибка).
func (c *AuthClient) SetPendingCampaign(ctx context.Context, telegramID int64, slug string) (int64, error) {
	resp, err := c.client.SetPendingCampaign(ctx, &pb.SetPendingCampaignRequest{
		TelegramId: telegramID,
		Slug:       slug,
	})
	if err != nil {
		return 0, err
	}
	return resp.CampaignId, nil
}
