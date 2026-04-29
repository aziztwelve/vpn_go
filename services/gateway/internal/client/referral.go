package client

import (
	"context"
	"fmt"

	pb "github.com/vpn/shared/pkg/proto/referral/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ReferralClient — обёртка над gRPC-клиентом referral-service.
// Используется handler'ами Gateway для проксирования REST → gRPC.
type ReferralClient struct {
	client pb.ReferralServiceClient
	conn   *grpc.ClientConn
	logger *zap.Logger
}

func NewReferralClient(addr string, logger *zap.Logger) (*ReferralClient, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("connect referral service: %w", err)
	}
	return &ReferralClient{
		client: pb.NewReferralServiceClient(conn),
		conn:   conn,
		logger: logger,
	}, nil
}

func (c *ReferralClient) Close() error {
	return c.conn.Close()
}

func (c *ReferralClient) GetOrCreateLink(ctx context.Context, userID int64) (*pb.GetOrCreateReferralLinkResponse, error) {
	return c.client.GetOrCreateReferralLink(ctx, &pb.GetOrCreateReferralLinkRequest{UserId: userID})
}

func (c *ReferralClient) GetStats(ctx context.Context, userID int64) (*pb.GetReferralStatsResponse, error) {
	return c.client.GetReferralStats(ctx, &pb.GetReferralStatsRequest{UserId: userID})
}

func (c *ReferralClient) CreateWithdrawal(ctx context.Context, userID int64, amount, method string, details map[string]string) (*pb.CreateWithdrawalRequestResponse, error) {
	return c.client.CreateWithdrawalRequest(ctx, &pb.CreateWithdrawalRequestRequest{
		UserId:         userID,
		AmountRub:      amount,
		PaymentMethod:  method,
		PaymentDetails: details,
	})
}

func (c *ReferralClient) ListWithdrawals(ctx context.Context, userID int64, status string, limit, offset int32) (*pb.ListWithdrawalRequestsResponse, error) {
	return c.client.ListWithdrawalRequests(ctx, &pb.ListWithdrawalRequestsRequest{
		UserId: userID,
		Status: status,
		Limit:  limit,
		Offset: offset,
	})
}
