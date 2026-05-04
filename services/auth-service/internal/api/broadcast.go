// Package api — broadcast.go: gRPC ручки для управления retention-рассылками.
//
// Вызываются gateway'ем:
//   - bot-callback (bc_approve_<id> / bc_cancel_<id>) → ApproveBroadcast/Cancel
//   - HTTP admin (/api/v1/admin/broadcasts/...) → List/Get/Update/Approve/Cancel
//
// Авторизация: Auth.admin_telegram_id (из CallbackQuery.From.ID) ИЛИ
// Auth.admin_user_id (из JWT.user_id). Ровно один из них должен быть
// != 0; auth-service сам сверяет с users.role='admin'.
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

// authorize резолвит Auth → users.id и проверяет role='admin'.
// Возвращает adminUserID (или 0) и status-error если не админ.
//
// Ровно один из admin_telegram_id / admin_user_id должен быть != 0.
// Если оба пусты — InvalidArgument; если оба заданы — берём user_id
// (более конкретный, JWT-источник).
func (a *BroadcastAPI) authorize(ctx context.Context, auth *pb.Auth) (int64, error) {
	if auth == nil {
		return 0, status.Error(codes.InvalidArgument, "auth required")
	}
	if auth.AdminUserId == 0 && auth.AdminTelegramId == 0 {
		return 0, status.Error(codes.InvalidArgument, "admin_telegram_id or admin_user_id required")
	}

	if auth.AdminUserId != 0 {
		ok, err := a.repo.IsAdminByUserID(ctx, auth.AdminUserId)
		if err != nil {
			return 0, status.Error(codes.PermissionDenied, "admin not found")
		}
		if !ok {
			return 0, status.Error(codes.PermissionDenied, "admin role required")
		}
		return auth.AdminUserId, nil
	}

	ok, userID, err := a.repo.IsAdmin(ctx, auth.AdminTelegramId)
	if err != nil {
		return 0, status.Error(codes.PermissionDenied, "admin not found")
	}
	if !ok {
		return 0, status.Error(codes.PermissionDenied, "admin role required")
	}
	return userID, nil
}

func (a *BroadcastAPI) ApproveBroadcast(
	ctx context.Context,
	req *pb.ApproveBroadcastRequest,
) (*pb.ApproveBroadcastResponse, error) {
	if req.DraftId == 0 {
		return nil, status.Error(codes.InvalidArgument, "draft_id required")
	}
	adminUserID, err := a.authorize(ctx, req.Auth)
	if err != nil {
		return nil, err
	}

	draft, err := a.repo.GetDraft(ctx, req.DraftId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "draft not found")
	}
	if draft.Status != "draft" {
		return nil, status.Errorf(codes.FailedPrecondition,
			"draft is in status %q, only 'draft' can be approved", draft.Status)
	}

	// 'draft' → 'approved' атомарно (CAS).
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
	// после возврата — это нормально.
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
	if req.DraftId == 0 {
		return nil, status.Error(codes.InvalidArgument, "draft_id required")
	}
	if _, err := a.authorize(ctx, req.Auth); err != nil {
		return nil, err
	}

	affected, err := a.repo.UpdateDraftStatus(ctx, req.DraftId, "draft", "cancelled", 0)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to cancel")
	}
	if affected == 0 {
		return nil, status.Error(codes.FailedPrecondition,
			"draft is not in 'draft' status (probably already approved/sent)")
	}

	a.logger.Info("CancelBroadcast: cancelled", zap.Int64("draft_id", req.DraftId))

	return &pb.CancelBroadcastResponse{
		DraftId: req.DraftId,
		Status:  "cancelled",
	}, nil
}

func (a *BroadcastAPI) GetDraftSummary(
	ctx context.Context,
	req *pb.GetDraftSummaryRequest,
) (*pb.DraftSummary, error) {
	if req.DraftId == 0 {
		return nil, status.Error(codes.InvalidArgument, "draft_id required")
	}
	if _, err := a.authorize(ctx, req.Auth); err != nil {
		return nil, err
	}

	d, err := a.repo.GetDraft(ctx, req.DraftId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "draft not found")
	}
	return draftToSummaryProto(d), nil
}

func (a *BroadcastAPI) ListBroadcasts(
	ctx context.Context,
	req *pb.ListBroadcastsRequest,
) (*pb.ListBroadcastsResponse, error) {
	if _, err := a.authorize(ctx, req.Auth); err != nil {
		return nil, err
	}

	items, total, err := a.repo.ListDrafts(ctx, req.StatusFilter, req.SegmentFilter, req.Limit, req.Offset)
	if err != nil {
		a.logger.Error("ListBroadcasts: failed", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to list")
	}

	out := make([]*pb.DraftSummary, 0, len(items))
	for i := range items {
		out = append(out, draftSummaryToProto(&items[i]))
	}
	return &pb.ListBroadcastsResponse{Items: out, Total: total}, nil
}

func (a *BroadcastAPI) GetBroadcastDetails(
	ctx context.Context,
	req *pb.GetBroadcastDetailsRequest,
) (*pb.BroadcastDetails, error) {
	if req.DraftId == 0 {
		return nil, status.Error(codes.InvalidArgument, "draft_id required")
	}
	if _, err := a.authorize(ctx, req.Auth); err != nil {
		return nil, err
	}

	d, err := a.repo.GetDraft(ctx, req.DraftId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "draft not found")
	}
	stats, err := a.repo.GetDraftStats(ctx, req.DraftId)
	if err != nil {
		a.logger.Error("GetBroadcastDetails: stats failed", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to get stats")
	}

	resp := &pb.BroadcastDetails{
		Id:             d.ID,
		SegmentKey:     d.SegmentKey,
		Title:          d.Title,
		BodyTemplate:   d.BodyTemplate,
		Buttons:        buttonsToProto(d.ButtonConfig),
		RecipientCount: int32(d.RecipientCount),
		Status:         d.Status,
		CreatedAtUnix:  d.CreatedAt.Unix(),
		Stats: &pb.Stats{
			Sent: stats.Sent, Blocked: stats.Blocked, Failed: stats.Failed,
			Opened: stats.Opened, Clicked: stats.Clicked,
		},
	}
	if d.ApprovedAt != nil {
		resp.ApprovedAtUnix = d.ApprovedAt.Unix()
	}
	if d.ApprovedBy != nil {
		resp.ApprovedByUserId = *d.ApprovedBy
	}
	if d.SentAt != nil {
		resp.SentAtUnix = d.SentAt.Unix()
	}
	return resp, nil
}

func (a *BroadcastAPI) UpdateBroadcast(
	ctx context.Context,
	req *pb.UpdateBroadcastRequest,
) (*pb.DraftSummary, error) {
	if req.DraftId == 0 {
		return nil, status.Error(codes.InvalidArgument, "draft_id required")
	}
	if _, err := a.authorize(ctx, req.Auth); err != nil {
		return nil, err
	}

	in := repository.UpdateDraftFieldsInput{
		Title:        req.Title,
		BodyTemplate: req.BodyTemplate,
	}
	// proto-семантика: nil buttons = не менять, [] = очистить. На уровне
	// gRPC nil и [] оба превращаются в len()==0, разницы нет. Принимаем
	// конвенцию: если в запросе указали buttons (пусть и пустые) — менять.
	// Для "не менять" клиент просто не передаёт поле (proto3 default = nil).
	if req.Buttons != nil {
		in.ButtonsSet = true
		in.Buttons = make([]repository.ButtonConfig, 0, len(req.Buttons))
		for _, b := range req.Buttons {
			in.Buttons = append(in.Buttons, repository.ButtonConfig{
				Text: b.Text, Type: b.Type, URL: b.Url, Data: b.Data,
			})
		}
	}

	affected, err := a.repo.UpdateDraftFields(ctx, req.DraftId, in)
	if err != nil {
		a.logger.Error("UpdateBroadcast: failed", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to update")
	}
	if affected == 0 {
		return nil, status.Error(codes.FailedPrecondition,
			"draft not found or not in 'draft' status")
	}

	d, err := a.repo.GetDraft(ctx, req.DraftId)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to refetch")
	}
	return draftToSummaryProto(d), nil
}

// ─── helpers ────────────────────────────────────────────────────────

func draftToSummaryProto(d *repository.Draft) *pb.DraftSummary {
	out := &pb.DraftSummary{
		Id:             d.ID,
		SegmentKey:     d.SegmentKey,
		Title:          d.Title,
		RecipientCount: int32(d.RecipientCount),
		Status:         d.Status,
		CreatedAtUnix:  d.CreatedAt.Unix(),
	}
	if d.SentAt != nil {
		out.SentAtUnix = d.SentAt.Unix()
	}
	return out
}

func draftSummaryToProto(d *repository.DraftSummary) *pb.DraftSummary {
	out := &pb.DraftSummary{
		Id:             d.ID,
		SegmentKey:     d.SegmentKey,
		Title:          d.Title,
		RecipientCount: int32(d.RecipientCount),
		Status:         d.Status,
		CreatedAtUnix:  d.CreatedAt.Unix(),
	}
	if d.SentAt != nil {
		out.SentAtUnix = d.SentAt.Unix()
	}
	return out
}

func buttonsToProto(in []repository.ButtonConfig) []*pb.Button {
	if len(in) == 0 {
		return nil
	}
	out := make([]*pb.Button, 0, len(in))
	for _, b := range in {
		out = append(out, &pb.Button{
			Text: b.Text, Type: b.Type, Url: b.URL, Data: b.Data,
		})
	}
	return out
}
