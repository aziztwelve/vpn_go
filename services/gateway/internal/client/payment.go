package client

import (
	"context"
	"fmt"

	pb "github.com/vpn/shared/pkg/proto/payment/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type PaymentClient struct {
	client pb.PaymentServiceClient
	conn   *grpc.ClientConn
	logger *zap.Logger
}

func NewPaymentClient(addr string, logger *zap.Logger) (*PaymentClient, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to payment service: %w", err)
	}
	return &PaymentClient{
		client: pb.NewPaymentServiceClient(conn),
		conn:   conn,
		logger: logger,
	}, nil
}

func (c *PaymentClient) Close() error {
	return c.conn.Close()
}

func (c *PaymentClient) CreateInvoice(ctx context.Context, userID int64, planID, maxDevices int32, provider string) (*pb.CreateInvoiceResponse, error) {
	return c.client.CreateInvoice(ctx, &pb.CreateInvoiceRequest{
		UserId:     userID,
		PlanId:     planID,
		MaxDevices: maxDevices,
		Provider:   provider,
	})
}

func (c *PaymentClient) ListUserPayments(ctx context.Context, userID int64, limit, offset int32) (*pb.ListUserPaymentsResponse, error) {
	return c.client.ListUserPayments(ctx, &pb.ListUserPaymentsRequest{
		UserId: userID,
		Limit:  limit,
		Offset: offset,
	})
}

func (c *PaymentClient) GetPayment(ctx context.Context, paymentID int64) (*pb.GetPaymentResponse, error) {
	return c.client.GetPayment(ctx, &pb.GetPaymentRequest{
		PaymentId: paymentID,
	})
}

func (c *PaymentClient) HandleTelegramUpdate(ctx context.Context, rawJSON []byte) (*pb.HandleTelegramUpdateResponse, error) {
	return c.client.HandleTelegramUpdate(ctx, &pb.HandleTelegramUpdateRequest{
		UpdateJson: rawJSON,
	})
}

func (c *PaymentClient) HandleWebhook(ctx context.Context, provider string, payload []byte, signature string) (*pb.HandleWebhookResponse, error) {
	return c.client.HandleWebhook(ctx, &pb.HandleWebhookRequest{
		Provider:  provider,
		Payload:   payload,
		Signature: signature,
	})
}
