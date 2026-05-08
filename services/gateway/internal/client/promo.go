// Package client — promo.go: gRPC-клиент к PromoService.
//
// PromoService живёт в auth-service бинарнике (один порт, общая БД), так
// что переиспользуем `*grpc.ClientConn` от AuthClient. Отдельный коннект
// здесь не открывается (см. NewPromoClient).
package client

import (
	"context"

	pb "github.com/vpn/shared/pkg/proto/promo/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type PromoClient struct {
	client pb.PromoServiceClient
	logger *zap.Logger
}

// NewPromoClient берёт grpc.ClientConn от AuthClient'а (так же как
// BroadcastClient). Закрытие — ответственность AuthClient'а.
func NewPromoClient(conn *grpc.ClientConn, logger *zap.Logger) *PromoClient {
	return &PromoClient{
		client: pb.NewPromoServiceClient(conn),
		logger: logger,
	}
}

// ─── Используется ботом (admin-команды) ───────────────────────────

// IssuePromoByTelegramID — для bot-команды /promo @username.
// adminTGID берётся из Message.From.ID.
func (c *PromoClient) IssuePromoByTelegramID(
	ctx context.Context,
	adminTGID, userID int64,
	planID, maxDevices int32,
	ttlSeconds int64,
) (*pb.IssuePromoResponse, error) {
	return c.client.IssuePromo(ctx, &pb.IssuePromoRequest{
		UserId:     userID,
		PlanId:     planID,
		MaxDevices: maxDevices,
		TtlSeconds: ttlSeconds,
		Auth:       &pb.Auth{AdminTelegramId: adminTGID},
	})
}

// GetPromoStatusByTelegramID — для bot-команды /promo_status @username.
func (c *PromoClient) GetPromoStatusByTelegramID(
	ctx context.Context,
	adminTGID, userID int64,
	planID int32,
) (*pb.GetPromoStatusResponse, error) {
	return c.client.GetPromoStatus(ctx, &pb.GetPromoStatusRequest{
		UserId: userID,
		PlanId: planID,
		Auth:   &pb.Auth{AdminTelegramId: adminTGID},
	})
}

// LookupUserByUsername — резолв @username → user_id+telegram_id.
// Используется ботом перед IssuePromo, чтобы превратить /promo @aziz в
// конкретный user.id для записи в promo_codes.
func (c *PromoClient) LookupUserByUsername(
	ctx context.Context,
	adminTGID int64,
	username string,
) (*pb.LookupUserResponse, error) {
	return c.client.LookupUser(ctx, &pb.LookupUserRequest{
		Username: username,
		Auth:     &pb.Auth{AdminTelegramId: adminTGID},
	})
}

// LookupUserByTelegramID — резолв telegram_id → user_id (когда админ
// указал ID цифрами вместо @username).
func (c *PromoClient) LookupUserByTelegramID(
	ctx context.Context,
	adminTGID, targetTGID int64,
) (*pb.LookupUserResponse, error) {
	return c.client.LookupUser(ctx, &pb.LookupUserRequest{
		TelegramId: targetTGID,
		Auth:       &pb.Auth{AdminTelegramId: adminTGID},
	})
}

// ─── Используется публичным /promo/p/{token} handler'ом ──────────

// Redeem — без auth, валидирует token и возвращает данные для CreateInvoice.
// status.NotFound если не существует, FailedPrecondition если expired.
func (c *PromoClient) Redeem(ctx context.Context, token string) (*pb.RedeemPromoResponse, error) {
	return c.client.RedeemPromo(ctx, &pb.RedeemPromoRequest{Token: token})
}

// AttachPayment — после успешного CreateInvoice прикрепить payment_id.
// Idempotent: повторный вызов с тем же payment_id — ok.
func (c *PromoClient) AttachPayment(ctx context.Context, token string, paymentID int64) error {
	_, err := c.client.AttachPayment(ctx, &pb.AttachPaymentRequest{
		Token:     token,
		PaymentId: paymentID,
	})
	return err
}

// ─── Используется payment-service по paid-webhook'у ──────────────

// MarkUsed — без auth, помечает promo по payment_id как использованный.
// Возвращает matched=false если payment не связан с промо.
func (c *PromoClient) MarkUsed(ctx context.Context, paymentID int64) (*pb.MarkUsedResponse, error) {
	return c.client.MarkUsed(ctx, &pb.MarkUsedRequest{PaymentId: paymentID})
}
