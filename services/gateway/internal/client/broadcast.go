// Package client — broadcast.go: gRPC-клиент к BroadcastService.
//
// BroadcastService живёт в auth-service бинарнике (один порт, общая БД), так
// что переиспользуем `*grpc.ClientConn` от AuthClient. Отдельный коннект
// здесь не открывается.
//
// authFromTelegramID / authFromUserID — конструкторы Auth-сообщения для
// двух call-flows (бот-callback и HTTP админка соответственно).
package client

import (
	"context"

	pb "github.com/vpn/shared/pkg/proto/broadcast/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type BroadcastClient struct {
	client pb.BroadcastServiceClient
	logger *zap.Logger
}

// NewBroadcastClient берёт grpc.ClientConn от AuthClient'а (в app.go это
// a.authClient.Conn()). Закрытие — ответственность AuthClient'а.
func NewBroadcastClient(conn *grpc.ClientConn, logger *zap.Logger) *BroadcastClient {
	return &BroadcastClient{
		client: pb.NewBroadcastServiceClient(conn),
		logger: logger,
	}
}

// authFromTelegramID — для callback-handler'ов бота (admin берётся из
// CallbackQuery.From.ID).
func authFromTelegramID(tgID int64) *pb.Auth {
	return &pb.Auth{AdminTelegramId: tgID}
}

// authFromUserID — для HTTP-handler'ов (admin берётся из JWT.user_id).
func authFromUserID(userID int64) *pb.Auth {
	return &pb.Auth{AdminUserId: userID}
}

// ─── Используется ботом (callback'и) ───────────────────────────────

func (c *BroadcastClient) ApproveBroadcastByTelegramID(ctx context.Context, draftID, adminTGID int64) (*pb.ApproveBroadcastResponse, error) {
	return c.client.ApproveBroadcast(ctx, &pb.ApproveBroadcastRequest{
		DraftId: draftID,
		Auth:    authFromTelegramID(adminTGID),
	})
}

func (c *BroadcastClient) CancelBroadcastByTelegramID(ctx context.Context, draftID, adminTGID int64) (*pb.CancelBroadcastResponse, error) {
	return c.client.CancelBroadcast(ctx, &pb.CancelBroadcastRequest{
		DraftId: draftID,
		Auth:    authFromTelegramID(adminTGID),
	})
}

// ListBroadcastsByTelegramID — для bot-команды /admin (admin берётся из
// Message.From.ID). Параметры идентичны ListBroadcasts (HTTP-вариант).
func (c *BroadcastClient) ListBroadcastsByTelegramID(
	ctx context.Context,
	adminTGID int64,
	statusFilter, segmentFilter string,
	limit, offset int32,
) (*pb.ListBroadcastsResponse, error) {
	return c.client.ListBroadcasts(ctx, &pb.ListBroadcastsRequest{
		Auth:          authFromTelegramID(adminTGID),
		StatusFilter:  statusFilter,
		SegmentFilter: segmentFilter,
		Limit:         limit,
		Offset:        offset,
	})
}

// GetBroadcastDetailsByTelegramID — для bot-команды /broadcast_stats <id>.
func (c *BroadcastClient) GetBroadcastDetailsByTelegramID(
	ctx context.Context,
	draftID, adminTGID int64,
) (*pb.BroadcastDetails, error) {
	return c.client.GetBroadcastDetails(ctx, &pb.GetBroadcastDetailsRequest{
		DraftId: draftID,
		Auth:    authFromTelegramID(adminTGID),
	})
}

// ─── Используется HTTP-админкой ────────────────────────────────────

func (c *BroadcastClient) ApproveBroadcast(ctx context.Context, draftID, adminUserID int64) (*pb.ApproveBroadcastResponse, error) {
	return c.client.ApproveBroadcast(ctx, &pb.ApproveBroadcastRequest{
		DraftId: draftID,
		Auth:    authFromUserID(adminUserID),
	})
}

func (c *BroadcastClient) CancelBroadcast(ctx context.Context, draftID, adminUserID int64) (*pb.CancelBroadcastResponse, error) {
	return c.client.CancelBroadcast(ctx, &pb.CancelBroadcastRequest{
		DraftId: draftID,
		Auth:    authFromUserID(adminUserID),
	})
}

func (c *BroadcastClient) ListBroadcasts(
	ctx context.Context,
	adminUserID int64,
	statusFilter, segmentFilter string,
	limit, offset int32,
) (*pb.ListBroadcastsResponse, error) {
	return c.client.ListBroadcasts(ctx, &pb.ListBroadcastsRequest{
		Auth:          authFromUserID(adminUserID),
		StatusFilter:  statusFilter,
		SegmentFilter: segmentFilter,
		Limit:         limit,
		Offset:        offset,
	})
}

func (c *BroadcastClient) GetBroadcastDetails(ctx context.Context, draftID, adminUserID int64) (*pb.BroadcastDetails, error) {
	return c.client.GetBroadcastDetails(ctx, &pb.GetBroadcastDetailsRequest{
		DraftId: draftID,
		Auth:    authFromUserID(adminUserID),
	})
}

func (c *BroadcastClient) UpdateBroadcast(
	ctx context.Context,
	draftID, adminUserID int64,
	title, body string,
	buttons []*pb.Button, // nil = не менять
) (*pb.DraftSummary, error) {
	return c.client.UpdateBroadcast(ctx, &pb.UpdateBroadcastRequest{
		DraftId:      draftID,
		Auth:         authFromUserID(adminUserID),
		Title:        title,
		BodyTemplate: body,
		Buttons:      buttons,
	})
}
