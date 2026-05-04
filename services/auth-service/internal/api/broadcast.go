// Package api — broadcast.go: gRPC ручки для approve/cancel retention-рассылок.
//
// Вызываются gateway'ем при обработке callback'а Telegram-кнопки
// (bc_approve_<id> / bc_cancel_<id>). Защита: admin_telegram_id из
// CallbackQuery.From.ID, мы внутри сравниваем с users.role='admin'.
package api

import (
	"context"

	"github.com/vpn/auth-service/internal/repository"
	"github.com/vpn/auth-service/internal/service"
	pb "github.com/vpn/shared/pkg/proto/broadcast/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// BroadcastAPI — gRPC handler. Sender запускается в фоновой горутине,
// поэтому ApproveBroadcast возвращается мгновенно (gateway-callback
// от Telegram имеет жёсткий таймаут).
type BroadcastAPI struct {
	pb.UnimplementedBroadcastServiceServer
	repo   *repository.BroadcastRepository
	sender *service.BroadcastSender
	logger *zap.Logger
}

func NewBroadcastAPI(
	repo *repository.BroadcastRepository,
	sender *service.BroadcastSender,
	logger *zap.Logger,
) *BroadcastAPI {
	return &BroadcastAPI{repo: repo, sender: sender, logger: logger}
}

func (a *BroadcastAPI) ApproveBroadcast(
	ctx context.Context,
	req *pb.ApproveBroadcastRequest,
) (*pb.ApproveBroadcastResponse, error) {
	if req.DraftId == 0 || req.AdminTelegramId == 0 {
		return nil, status.Error(codes.InvalidArgument, "draft_id and admin_telegram_id required")
	}

	isAdmin, adminUserID, err := a.repo.IsAdmin(ctx, req.AdminTelegramId)
	if err != nil {
		a.logger.Warn("ApproveBroadcast: admin lookup failed",
			zap.Int64("admin_tg_id", req.AdminTelegramId), zap.Error(err))
		return nil, status.Error(codes.PermissionDenied, "admin not found")
	}
	if !isAdmin {
		return nil, status.Error(codes.PermissionDenied, "admin role required")
	}

	draft, err := a.repo.GetDraft(ctx, req.DraftId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "draft not found")
	}
	if draft.Status != "draft" {
		return nil, status.Errorf(codes.FailedPrecondition,
			"draft is in status %q, only 'draft' can be approved", draft.Status)
	}

	// 'draft' → 'approved' атомарно. Если кто-то параллельно cancelled,
	// affected=0 и мы вернём FailedPrecondition.
	affected, err := a.repo.UpdateDraftStatus(ctx, req.DraftId, "draft", "approved", adminUserID)
	if err != nil {
		a.logger.Error("ApproveBroadcast: update status failed",
			zap.Int64("draft_id", req.DraftId), zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to approve")
	}
	if affected == 0 {
		return nil, status.Error(codes.FailedPrecondition, "draft already approved or cancelled")
	}

	a.logger.Info("ApproveBroadcast: approved, launching sender",
		zap.Int64("draft_id", req.DraftId),
		zap.Int64("admin_user_id", adminUserID),
		zap.Int("recipients", draft.RecipientCount),
	)

	// Sender в фоне с detached context. ctx из gRPC отменится сразу
	// после возврата — это нормально, sender не должен от него зависеть.
	go func() {
		bgCtx := context.Background()
		if _, err := a.sender.Send(bgCtx, req.DraftId); err != nil {
			a.logger.Error("BroadcastSender: failed",
				zap.Int64("draft_id", req.DraftId), zap.Error(err))
		}
	}()

	return &pb.ApproveBroadcastResponse{
		DraftId:        req.DraftId,
		Status:         "approved",
		RecipientCount: int32(draft.RecipientCount),
	}, nil
}

func (a *BroadcastAPI) CancelBroadcast(
	ctx context.Context,
	req *pb.CancelBroadcastRequest,
) (*pb.CancelBroadcastResponse, error) {
	if req.DraftId == 0 || req.AdminTelegramId == 0 {
		return nil, status.Error(codes.InvalidArgument, "draft_id and admin_telegram_id required")
	}

	isAdmin, _, err := a.repo.IsAdmin(ctx, req.AdminTelegramId)
	if err != nil || !isAdmin {
		return nil, status.Error(codes.PermissionDenied, "admin role required")
	}

	// Только из 'draft' можно отменить — после approve уже sender работает.
	affected, err := a.repo.UpdateDraftStatus(ctx, req.DraftId, "draft", "cancelled", 0)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to cancel")
	}
	if affected == 0 {
		return nil, status.Error(codes.FailedPrecondition,
			"draft is not in 'draft' status (probably already approved/sent)")
	}

	a.logger.Info("CancelBroadcast: cancelled",
		zap.Int64("draft_id", req.DraftId),
		zap.Int64("admin_tg_id", req.AdminTelegramId),
	)

	return &pb.CancelBroadcastResponse{
		DraftId: req.DraftId,
		Status:  "cancelled",
	}, nil
}

func (a *BroadcastAPI) GetDraftSummary(
	ctx context.Context,
	req *pb.GetDraftSummaryRequest,
) (*pb.DraftSummary, error) {
	if req.DraftId == 0 || req.AdminTelegramId == 0 {
		return nil, status.Error(codes.InvalidArgument, "draft_id and admin_telegram_id required")
	}
	isAdmin, _, err := a.repo.IsAdmin(ctx, req.AdminTelegramId)
	if err != nil || !isAdmin {
		return nil, status.Error(codes.PermissionDenied, "admin role required")
	}

	d, err := a.repo.GetDraft(ctx, req.DraftId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "draft not found")
	}
	resp := &pb.DraftSummary{
		Id:             d.ID,
		SegmentKey:     d.SegmentKey,
		Title:          d.Title,
		RecipientCount: int32(d.RecipientCount),
		Status:         d.Status,
		CreatedAtUnix:  d.CreatedAt.Unix(),
	}
	if d.SentAt != nil {
		resp.SentAtUnix = d.SentAt.Unix()
	}
	return resp, nil
}
