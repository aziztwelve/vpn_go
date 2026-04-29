package api

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/vpn/referral-service/internal/model"
	"github.com/vpn/referral-service/internal/service"
	pb "github.com/vpn/shared/pkg/proto/referral/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type API struct {
	pb.UnimplementedReferralServiceServer
	svc *service.Referral
	log *zap.Logger
}

func New(svc *service.Referral, log *zap.Logger) *API {
	return &API{svc: svc, log: log}
}

// ─── GetOrCreateReferralLink ────────────────────────────────────────

func (a *API) GetOrCreateReferralLink(ctx context.Context, req *pb.GetOrCreateReferralLinkRequest) (*pb.GetOrCreateReferralLinkResponse, error) {
	if req.UserId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}
	info, err := a.svc.GetOrCreateLink(ctx, req.UserId)
	if err != nil {
		a.log.Error("GetOrCreateLink failed", zap.Int64("user_id", req.UserId), zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to get or create link")
	}
	return &pb.GetOrCreateReferralLinkResponse{
		Url:        info.URL,
		Token:      info.Token,
		ClickCount: info.ClickCount,
	}, nil
}

// ─── RegisterClick ──────────────────────────────────────────────────

func (a *API) RegisterClick(ctx context.Context, req *pb.RegisterClickRequest) (*pb.RegisterClickResponse, error) {
	if req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}
	found, clicks, err := a.svc.RegisterClick(ctx, req.Token)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to register click")
	}
	return &pb.RegisterClickResponse{Found: found, ClickCount: clicks}, nil
}

// ─── RegisterReferral ───────────────────────────────────────────────

func (a *API) RegisterReferral(ctx context.Context, req *pb.RegisterReferralRequest) (*pb.RegisterReferralResponse, error) {
	if req.InvitedUserId == 0 {
		return nil, status.Error(codes.InvalidArgument, "invited_user_id is required")
	}
	if req.InviterToken == "" {
		return &pb.RegisterReferralResponse{SkipReason: model.SkipReasonTokenNotFound}, nil
	}
	res, err := a.svc.RegisterReferral(ctx, req.InviterToken, req.InvitedUserId)
	if err != nil {
		a.log.Error("RegisterReferral failed",
			zap.String("token", req.InviterToken),
			zap.Int64("invited_id", req.InvitedUserId),
			zap.Error(err),
		)
		return nil, status.Error(codes.Internal, "failed to register referral")
	}
	return &pb.RegisterReferralResponse{
		Registered:         res.Registered,
		InviterUserId:      res.InviterUserID,
		SkipReason:         res.SkipReason,
		InviterDaysAwarded: res.InviterDaysAwarded,
		InvitedDaysAwarded: res.InvitedDaysAwarded,
	}, nil
}

// ─── ApplyBonus ─────────────────────────────────────────────────────

func (a *API) ApplyBonus(ctx context.Context, req *pb.ApplyBonusRequest) (*pb.ApplyBonusResponse, error) {
	if req.InvitedUserId == 0 || req.PaymentId == 0 {
		return nil, status.Error(codes.InvalidArgument, "invited_user_id and payment_id are required")
	}
	amount, err := parseAmount(req.PaymentAmountRub)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid payment_amount_rub: %v", err)
	}

	res, err := a.svc.ApplyBonus(ctx, req.InvitedUserId, amount, req.PaymentId)
	if err != nil {
		a.log.Error("ApplyBonus failed",
			zap.Int64("invited_id", req.InvitedUserId),
			zap.Int64("payment_id", req.PaymentId),
			zap.Error(err),
		)
		return nil, status.Error(codes.Internal, "failed to apply bonus")
	}
	return &pb.ApplyBonusResponse{
		Applied:           res.Applied,
		AlreadyApplied:    res.AlreadyApplied,
		NoRelationship:    res.NoRelationship,
		InviterUserId:     res.InviterUserID,
		InviterRole:       res.InviterRole,
		BalanceAmountRub:  fmt.Sprintf("%.2f", res.BalanceAmount),
	}, nil
}

// ─── GetReferralStats ───────────────────────────────────────────────

func (a *API) GetReferralStats(ctx context.Context, req *pb.GetReferralStatsRequest) (*pb.GetReferralStatsResponse, error) {
	if req.UserId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}
	st, err := a.svc.GetStats(ctx, req.UserId)
	if err != nil {
		a.log.Error("GetStats failed", zap.Int64("user_id", req.UserId), zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to get stats")
	}
	return &pb.GetReferralStatsResponse{
		InvitedCount:           st.InvitedCount,
		PurchasedCount:         st.PurchasedCount,
		RewardedDaysTotal:      st.RewardedDaysTotal,
		EarnedBalanceRubTotal:  fmt.Sprintf("%.2f", st.EarnedBalanceTotal),
		CurrentBalanceRub:      fmt.Sprintf("%.2f", st.CurrentBalance),
		PendingCount:           st.PendingCount,
	}, nil
}

// ─── Withdrawals ────────────────────────────────────────────────────

func (a *API) CreateWithdrawalRequest(ctx context.Context, req *pb.CreateWithdrawalRequestRequest) (*pb.CreateWithdrawalRequestResponse, error) {
	if req.UserId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}
	amount, err := parseAmount(req.AmountRub)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid amount_rub: %v", err)
	}
	if req.PaymentMethod == "" {
		return nil, status.Error(codes.InvalidArgument, "payment_method is required")
	}
	wr, errCode, err := a.svc.CreateWithdrawal(ctx, req.UserId, amount, req.PaymentMethod, req.PaymentDetails)
	if err != nil {
		a.log.Error("CreateWithdrawal failed", zap.Int64("user_id", req.UserId), zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to create withdrawal")
	}
	if errCode != "" {
		return &pb.CreateWithdrawalRequestResponse{Error: string(errCode)}, nil
	}
	return &pb.CreateWithdrawalRequestResponse{Request: modelWithdrawalToProto(wr)}, nil
}

func (a *API) ListWithdrawalRequests(ctx context.Context, req *pb.ListWithdrawalRequestsRequest) (*pb.ListWithdrawalRequestsResponse, error) {
	items, total, err := a.svc.ListWithdrawals(ctx, req.UserId, req.Status, req.Limit, req.Offset)
	if err != nil {
		a.log.Error("ListWithdrawals failed", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to list withdrawals")
	}
	out := make([]*pb.WithdrawalRequest, 0, len(items))
	for _, wr := range items {
		out = append(out, modelWithdrawalToProto(wr))
	}
	return &pb.ListWithdrawalRequestsResponse{Requests: out, Total: total}, nil
}

// ─── helpers ────────────────────────────────────────────────────────

func parseAmount(s string) (float64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty amount")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if v < 0 {
		return 0, fmt.Errorf("amount must be non-negative")
	}
	return v, nil
}

func modelWithdrawalToProto(wr *model.WithdrawalRequest) *pb.WithdrawalRequest {
	processed := ""
	if wr.ProcessedAt != nil {
		processed = wr.ProcessedAt.Format(time.RFC3339)
	}
	return &pb.WithdrawalRequest{
		Id:             wr.ID,
		UserId:         wr.UserID,
		AmountRub:      fmt.Sprintf("%.2f", wr.Amount),
		PaymentMethod: wr.PaymentMethod,
		PaymentDetails: wr.PaymentDetails,
		Status:         wr.Status,
		AdminComment:   wr.AdminComment,
		CreatedAt:      wr.CreatedAt.Format(time.RFC3339),
		ProcessedAt:    processed,
	}
}
