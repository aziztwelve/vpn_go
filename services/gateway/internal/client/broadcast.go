// Package client — broadcast.go: gRPC-клиент к BroadcastService.
//
// BroadcastService живёт в auth-service бинарнике (один порт, общая БД), так
// что переиспользуем `*grpc.ClientConn` от AuthClient. Отдельный коннект
// здесь не открывается.
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

// ApproveBroadcast — admin click "✅ Approve #N" → callback запускает sender.
// Возвращается мгновенно (sender в фоне на auth-service).
func (c *BroadcastClient) ApproveBroadcast(ctx context.Context, draftID, adminTelegramID int64) (*pb.ApproveBroadcastResponse, error) {
	return c.client.ApproveBroadcast(ctx, &pb.ApproveBroadcastRequest{
		DraftId:         draftID,
		AdminTelegramId: adminTelegramID,
	})
}

// CancelBroadcast — admin click "❌ Cancel #N". Применимо только к
// status='draft'; после approve cancel игнорируется (FailedPrecondition).
func (c *BroadcastClient) CancelBroadcast(ctx context.Context, draftID, adminTelegramID int64) (*pb.CancelBroadcastResponse, error) {
	return c.client.CancelBroadcast(ctx, &pb.CancelBroadcastRequest{
		DraftId:         draftID,
		AdminTelegramId: adminTelegramID,
	})
}
