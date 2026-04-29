package api

import (
	"context"
	"fmt"
	"math"

	"github.com/vpn/subscription-service/internal/model"
	"github.com/vpn/subscription-service/internal/service"
	pb "github.com/vpn/shared/pkg/proto/subscription/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// currencyStars — код "валюты" Telegram Stars в таблице currency_rates.
const currencyStars = "STARS"

// rubToStars переводит рублёвую цену в stars по курсу rateToRub
// (1 Star = rateToRub RUB). Округление вверх чтобы не недополучить.
func rubToStars(priceRub float64, rateToRub float64) int32 {
	if rateToRub <= 0 {
		return 0
	}
	return int32(math.Ceil(priceRub / rateToRub))
}

type SubscriptionAPI struct {
	pb.UnimplementedSubscriptionServiceServer
	service *service.SubscriptionService
	logger  *zap.Logger
}

func NewSubscriptionAPI(service *service.SubscriptionService, logger *zap.Logger) *SubscriptionAPI {
	return &SubscriptionAPI{
		service: service,
		logger:  logger,
	}
}

func (a *SubscriptionAPI) ListPlans(ctx context.Context, req *pb.ListPlansRequest) (*pb.ListPlansResponse, error) {
	plans, err := a.service.ListPlans(ctx, req.ActiveOnly)
	if err != nil {
		a.logger.Error("Failed to list plans", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to list plans")
	}

	starsRate, err := a.service.GetRateToRub(ctx, currencyStars)
	if err != nil {
		a.logger.Error("Failed to load STARS rate", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to load currency rate")
	}

	var pbPlans []*pb.SubscriptionPlan
	for _, plan := range plans {
		pbPlans = append(pbPlans, modelPlanToProto(plan, starsRate))
	}

	return &pb.ListPlansResponse{Plans: pbPlans}, nil
}

func (a *SubscriptionAPI) GetDevicePricing(ctx context.Context, req *pb.GetDevicePricingRequest) (*pb.GetDevicePricingResponse, error) {
	if req.PlanId == 0 {
		return nil, status.Error(codes.InvalidArgument, "plan_id is required")
	}

	prices, err := a.service.GetDevicePricing(ctx, req.PlanId)
	if err != nil {
		a.logger.Error("Failed to get device pricing", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to get pricing")
	}

	starsRate, err := a.service.GetRateToRub(ctx, currencyStars)
	if err != nil {
		a.logger.Error("Failed to load STARS rate", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to load currency rate")
	}

	var pbPrices []*pb.DevicePrice
	for _, price := range prices {
		pbPrices = append(pbPrices, &pb.DevicePrice{
			MaxDevices: price.MaxDevices,
			Price:      fmt.Sprintf("%.2f", price.Price),
			PriceStars: rubToStars(price.Price, starsRate),
			PlanName:   price.PlanName,
		})
	}

	return &pb.GetDevicePricingResponse{Prices: pbPrices}, nil
}

func (a *SubscriptionAPI) CreateSubscription(ctx context.Context, req *pb.CreateSubscriptionRequest) (*pb.CreateSubscriptionResponse, error) {
	if req.UserId == 0 || req.PlanId == 0 || req.MaxDevices == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id, plan_id, and max_devices are required")
	}

	// Parse total_price
	var totalPrice float64
	fmt.Sscanf(req.TotalPrice, "%f", &totalPrice)

	sub, err := a.service.CreateSubscription(ctx, req.UserId, req.PlanId, req.MaxDevices, totalPrice)
	if err != nil {
		a.logger.Error("Failed to create subscription", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to create subscription")
	}

	return &pb.CreateSubscriptionResponse{
		Subscription: modelSubscriptionToProto(sub),
	}, nil
}

func (a *SubscriptionAPI) GetActiveSubscription(ctx context.Context, req *pb.GetActiveSubscriptionRequest) (*pb.GetActiveSubscriptionResponse, error) {
	if req.UserId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	a.logger.Info("GetActiveSubscription called", zap.Int64("user_id", req.UserId))

	sub, hasActive, err := a.service.GetActiveSubscription(ctx, req.UserId)
	if err != nil {
		a.logger.Error("Failed to get active subscription", zap.Int64("user_id", req.UserId), zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to get subscription")
	}

	if !hasActive {
		a.logger.Info("No active subscription found", zap.Int64("user_id", req.UserId))
		return &pb.GetActiveSubscriptionResponse{HasActive: false}, nil
	}

	a.logger.Info("Active subscription found", zap.Int64("user_id", req.UserId), zap.Int64("sub_id", sub.ID))
	return &pb.GetActiveSubscriptionResponse{
		Subscription: modelSubscriptionToProto(sub),
		HasActive:    true,
	}, nil
}

func (a *SubscriptionAPI) ExtendSubscription(ctx context.Context, req *pb.ExtendSubscriptionRequest) (*pb.ExtendSubscriptionResponse, error) {
	if req.SubscriptionId == 0 || req.Days == 0 {
		return nil, status.Error(codes.InvalidArgument, "subscription_id and days are required")
	}

	sub, err := a.service.ExtendSubscription(ctx, req.SubscriptionId, req.Days)
	if err != nil {
		a.logger.Error("Failed to extend subscription", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to extend subscription")
	}

	return &pb.ExtendSubscriptionResponse{
		Subscription: modelSubscriptionToProto(sub),
	}, nil
}

func (a *SubscriptionAPI) CancelSubscription(ctx context.Context, req *pb.CancelSubscriptionRequest) (*pb.CancelSubscriptionResponse, error) {
	if req.SubscriptionId == 0 {
		return nil, status.Error(codes.InvalidArgument, "subscription_id is required")
	}

	sub, err := a.service.CancelSubscription(ctx, req.SubscriptionId)
	if err != nil {
		a.logger.Error("Failed to cancel subscription", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to cancel subscription")
	}

	return &pb.CancelSubscriptionResponse{
		Subscription: modelSubscriptionToProto(sub),
	}, nil
}

func (a *SubscriptionAPI) CheckSubscriptionActive(ctx context.Context, req *pb.CheckSubscriptionActiveRequest) (*pb.CheckSubscriptionActiveResponse, error) {
	if req.UserId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	isActive, subID, expiresAt, maxDevices, err := a.service.CheckSubscriptionActive(ctx, req.UserId)
	if err != nil {
		a.logger.Error("Failed to check subscription", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to check subscription")
	}

	return &pb.CheckSubscriptionActiveResponse{
		IsActive:       isActive,
		SubscriptionId: subID,
		ExpiresAt:      expiresAt,
		MaxDevices:     maxDevices,
	}, nil
}

func (a *SubscriptionAPI) GetSubscriptionHistory(ctx context.Context, req *pb.GetSubscriptionHistoryRequest) (*pb.GetSubscriptionHistoryResponse, error) {
	if req.UserId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	subs, err := a.service.GetSubscriptionHistory(ctx, req.UserId)
	if err != nil {
		a.logger.Error("Failed to get subscription history", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to get history")
	}

	var pbSubs []*pb.Subscription
	for _, sub := range subs {
		pbSubs = append(pbSubs, modelSubscriptionToProto(sub))
	}

	return &pb.GetSubscriptionHistoryResponse{Subscriptions: pbSubs}, nil
}

func (a *SubscriptionAPI) StartTrial(ctx context.Context, req *pb.StartTrialRequest) (*pb.StartTrialResponse, error) {
	if req.UserId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	sub, alreadyUsed, err := a.service.StartTrial(ctx, req.UserId)
	if err != nil {
		a.logger.Error("Failed to start trial", zap.Int64("user_id", req.UserId), zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to start trial")
	}

	if alreadyUsed {
		return &pb.StartTrialResponse{WasAlreadyUsed: true}, nil
	}

	return &pb.StartTrialResponse{
		Subscription:   modelSubscriptionToProto(sub),
		WasAlreadyUsed: false,
	}, nil
}

// modelPlanToProto — билдер proto из доменной модели.
// starsRate передаётся извне (один раз на запрос, чтобы не ходить в БД
// на каждый план) и используется для вычисления PriceStars = ceil(rub / rate).
func modelPlanToProto(plan *model.SubscriptionPlan, starsRate float64) *pb.SubscriptionPlan {
	return &pb.SubscriptionPlan{
		Id:           plan.ID,
		Name:         plan.Name,
		DurationDays: plan.DurationDays,
		MaxDevices:   plan.MaxDevices,
		BasePrice:    fmt.Sprintf("%.2f", plan.BasePrice),
		IsActive:     plan.IsActive,
		PriceStars:   rubToStars(plan.BasePrice, starsRate),
		IsTrial:      plan.IsTrial,
	}
}

func modelSubscriptionToProto(sub *model.Subscription) *pb.Subscription {
	return &pb.Subscription{
		Id:         sub.ID,
		UserId:     sub.UserID,
		PlanId:     sub.PlanID,
		PlanName:   sub.PlanName,
		MaxDevices: sub.MaxDevices,
		TotalPrice: fmt.Sprintf("%.2f", sub.TotalPrice),
		StartedAt:  sub.StartedAt.Format("2006-01-02T15:04:05Z"),
		ExpiresAt:  sub.ExpiresAt.Format("2006-01-02T15:04:05Z"),
		Status:     sub.Status,
		CreatedAt:  sub.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func (a *SubscriptionAPI) ApplyBonusDays(ctx context.Context, req *pb.ApplyBonusDaysRequest) (*pb.ApplyBonusDaysResponse, error) {
	if req.UserId == 0 || req.Days <= 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id and positive days are required")
	}

	sub, addedToPending, pendingTotal, err := a.service.ApplyBonusDays(ctx, req.UserId, req.Days)
	if err != nil {
		a.logger.Error("ApplyBonusDays failed",
			zap.Int64("user_id", req.UserId),
			zap.Int32("days", req.Days),
			zap.Error(err),
		)
		return nil, status.Error(codes.Internal, "failed to apply bonus days")
	}

	resp := &pb.ApplyBonusDaysResponse{
		PendingDaysTotal: pendingTotal,
	}
	if addedToPending {
		resp.AddedToPending = true
	} else {
		resp.AppliedToSubscription = true
		resp.ExtendedSubscription = modelSubscriptionToProto(sub)
	}
	return resp, nil
}

func (a *SubscriptionAPI) ClaimChannelBonus(ctx context.Context, req *pb.ClaimChannelBonusRequest) (*pb.ClaimChannelBonusResponse, error) {
	if req.UserId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	sub, alreadyClaimed, noActiveSub, err := a.service.ClaimChannelBonus(ctx, req.UserId)
	if err != nil {
		a.logger.Error("Failed to claim channel bonus", zap.Int64("user_id", req.UserId), zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to claim channel bonus")
	}

	if alreadyClaimed {
		return &pb.ClaimChannelBonusResponse{
			Success:        false,
			AlreadyClaimed: true,
		}, nil
	}

	if noActiveSub {
		return &pb.ClaimChannelBonusResponse{
			Success:              false,
			NoActiveSubscription: true,
		}, nil
	}

	return &pb.ClaimChannelBonusResponse{
		Success:      true,
		Subscription: modelSubscriptionToProto(sub),
	}, nil
}
