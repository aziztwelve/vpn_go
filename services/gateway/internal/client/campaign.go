package client

import (
	"context"

	pb "github.com/vpn/shared/pkg/proto/campaign/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// CampaignClient — обёртка над gRPC-клиентом CampaignService.
//
// Сервис хостится в том же бинарнике что и ReferralService (shared DB, shared
// gRPC-порт), поэтому клиент строится поверх уже существующего
// `*grpc.ClientConn` от ReferralClient'а — без дублирования TCP-коннектов.
type CampaignClient struct {
	client pb.CampaignServiceClient
	logger *zap.Logger
}

// NewCampaignClient принимает conn от ReferralClient'а (одна и та же таргет-машина).
// Закрытие соединения — ответственность владельца (ReferralClient.Close в app.go).
func NewCampaignClient(conn *grpc.ClientConn, logger *zap.Logger) *CampaignClient {
	return &CampaignClient{
		client: pb.NewCampaignServiceClient(conn),
		logger: logger,
	}
}

func (c *CampaignClient) CreateCampaign(ctx context.Context, req *pb.CreateCampaignRequest) (*pb.Campaign, error) {
	return c.client.CreateCampaign(ctx, req)
}

func (c *CampaignClient) UpdateCampaign(ctx context.Context, req *pb.UpdateCampaignRequest) (*pb.Campaign, error) {
	return c.client.UpdateCampaign(ctx, req)
}

func (c *CampaignClient) ArchiveCampaign(ctx context.Context, id int64) (*pb.Campaign, error) {
	return c.client.ArchiveCampaign(ctx, &pb.ArchiveCampaignRequest{Id: id})
}

func (c *CampaignClient) GetCampaign(ctx context.Context, id int64, from, to string) (*pb.CampaignWithStats, error) {
	return c.client.GetCampaign(ctx, &pb.GetCampaignRequest{Id: id, From: from, To: to})
}

func (c *CampaignClient) ListCampaigns(ctx context.Context, includeArchived bool, limit, offset int32) (*pb.ListCampaignsResponse, error) {
	return c.client.ListCampaigns(ctx, &pb.ListCampaignsRequest{
		IncludeArchived: includeArchived,
		Limit:           limit,
		Offset:          offset,
	})
}

func (c *CampaignClient) GetCampaignStats(ctx context.Context, id int64, from, to string) (*pb.CampaignStats, error) {
	return c.client.GetCampaignStats(ctx, &pb.GetCampaignStatsRequest{
		CampaignId: id,
		From:       from,
		To:         to,
	})
}
