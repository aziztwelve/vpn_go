package client

import (
	"context"
	"fmt"

	pb "github.com/vpn/shared/pkg/proto/subscription/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type SubscriptionClient struct {
	client pb.SubscriptionServiceClient
	conn   *grpc.ClientConn
	logger *zap.Logger
}

func NewSubscriptionClient(addr string, logger *zap.Logger) (*SubscriptionClient, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to subscription service: %w", err)
	}

	return &SubscriptionClient{
		client: pb.NewSubscriptionServiceClient(conn),
		conn:   conn,
		logger: logger,
	}, nil
}

func (c *SubscriptionClient) Close() error {
	return c.conn.Close()
}

func (c *SubscriptionClient) ListPlans(ctx context.Context, activeOnly bool) (*pb.ListPlansResponse, error) {
	return c.client.ListPlans(ctx, &pb.ListPlansRequest{
		ActiveOnly: activeOnly,
	})
}

func (c *SubscriptionClient) GetDevicePricing(ctx context.Context, planID int32) (*pb.GetDevicePricingResponse, error) {
	return c.client.GetDevicePricing(ctx, &pb.GetDevicePricingRequest{
		PlanId: planID,
	})
}

func (c *SubscriptionClient) GetActiveSubscription(ctx context.Context, userID int64) (*pb.GetActiveSubscriptionResponse, error) {
	return c.client.GetActiveSubscription(ctx, &pb.GetActiveSubscriptionRequest{
		UserId: userID,
	})
}

func (c *SubscriptionClient) CreateSubscription(ctx context.Context, userID int64, planID int32, maxDevices int32, totalPrice string) (*pb.CreateSubscriptionResponse, error) {
	return c.client.CreateSubscription(ctx, &pb.CreateSubscriptionRequest{
		UserId:     userID,
		PlanId:     planID,
		MaxDevices: maxDevices,
		TotalPrice: totalPrice,
	})
}

func (c *SubscriptionClient) GetSubscriptionHistory(ctx context.Context, userID int64) (*pb.GetSubscriptionHistoryResponse, error) {
	return c.client.GetSubscriptionHistory(ctx, &pb.GetSubscriptionHistoryRequest{
		UserId: userID,
	})
}

func (c *SubscriptionClient) StartTrial(ctx context.Context, userID int64) (*pb.StartTrialResponse, error) {
	return c.client.StartTrial(ctx, &pb.StartTrialRequest{UserId: userID})
}

func (c *SubscriptionClient) ClaimChannelBonus(ctx context.Context, req *pb.ClaimChannelBonusRequest) (*pb.ClaimChannelBonusResponse, error) {
	return c.client.ClaimChannelBonus(ctx, req)
}
