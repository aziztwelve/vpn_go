package api

import (
	"context"
	"fmt"

	"github.com/vpn/subscription-service/internal/model"
	"github.com/vpn/subscription-service/internal/service"
	pb "github.com/vpn/shared/pkg/proto/subscription/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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

	var pbPlans []*pb.SubscriptionPlan
	for _, plan := range plans {
		pbPlans = append(pbPlans, modelPlanToProto(plan))
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

	var pbPrices []*pb.DevicePrice
	for _, price := range prices {
		pbPrices = append(pbPrices, &pb.DevicePrice{
			MaxDevices: price.MaxDevices,
			Price:      fmt.Sprintf("%.2f", price.Price),
			PriceStars: price.PriceStars,
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

	sub, hasActive, err := a.service.GetActiveSubscription(ctx, req.UserId)
	if err != nil {
		a.logger.Error("Failed to get active subscription", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to get subscription")
	}

	if !hasActive {
		return &pb.GetActiveSubscriptionResponse{HasActive: false}, nil
	}

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

func modelPlanToProto(plan *model.SubscriptionPlan) *pb.SubscriptionPlan {
	return &pb.SubscriptionPlan{
		Id:           plan.ID,
		Name:         plan.Name,
		DurationDays: plan.DurationDays,
		MaxDevices:   plan.MaxDevices,
		BasePrice:    fmt.Sprintf("%.2f", plan.BasePrice),
		IsActive:     plan.IsActive,
		PriceStars:   plan.PriceStars,
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
