package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vpn/referral-service/internal/model"
	"github.com/vpn/referral-service/internal/repository"
	"github.com/vpn/referral-service/internal/service"
	pb "github.com/vpn/shared/pkg/proto/campaign/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CampaignAPI — gRPC handler для CampaignService.
//
// Регистрируется в том же бинарнике что и Referral API (shared DB, shared port).
type CampaignAPI struct {
	pb.UnimplementedCampaignServiceServer
	svc *service.CampaignService
	log *zap.Logger
}

func NewCampaign(svc *service.CampaignService, log *zap.Logger) *CampaignAPI {
	return &CampaignAPI{svc: svc, log: log}
}

// ─── CreateCampaign ────────────────────────────────────────────────

func (a *CampaignAPI) CreateCampaign(ctx context.Context, req *pb.CreateCampaignRequest) (*pb.Campaign, error) {
	c, err := a.svc.Create(ctx, service.CreateCampaignInput{
		Slug:              req.Slug,
		Name:              req.Name,
		Notes:             req.Notes,
		PartnerUserID:     req.PartnerUserId,
		PayoutPercent:     req.PayoutPercent,
		CreatedBy:         req.CreatedByUserId,
		TrialDurationDays: req.TrialDurationDays,
	})
	if err != nil {
		return nil, mapCampaignErr(err)
	}
	return a.toProto(c), nil
}

// ─── UpdateCampaign ────────────────────────────────────────────────

// Update-семантика для int64/int32: специальное значение -1 → "обнулить".
// Пустое значение (0/"") → "не менять". Для строк (name/notes) при пустой
// строке считаем "не менять" — изменение на пустую строку требует null'я,
// мы намеренно не поддерживаем (есть archive чтобы убрать кампанию совсем).
func (a *CampaignAPI) UpdateCampaign(ctx context.Context, req *pb.UpdateCampaignRequest) (*pb.Campaign, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	in := service.UpdateCampaignInput{}
	if req.Name != "" {
		n := req.Name
		in.Name = &n
	}
	if req.Notes != "" {
		n := req.Notes
		in.Notes = &n
	}

	switch {
	case req.PartnerUserId == -1:
		in.ClearPartner = true
		in.ClearPayout = true // нет партнёра → нет процента
	case req.PartnerUserId > 0:
		v := req.PartnerUserId
		in.PartnerUserID = &v
	}
	switch {
	case req.PayoutPercent == -1:
		in.ClearPayout = true
	case req.PayoutPercent > 0:
		v := req.PayoutPercent
		in.PayoutPercent = &v
	}
	switch {
	case req.TrialDurationDays == -1:
		in.ClearTrialDuration = true
	case req.TrialDurationDays > 0:
		v := req.TrialDurationDays
		in.TrialDurationDays = &v
	}

	c, err := a.svc.Update(ctx, req.Id, in)
	if err != nil {
		return nil, mapCampaignErr(err)
	}
	return a.toProto(c), nil
}

// ─── ArchiveCampaign ───────────────────────────────────────────────

func (a *CampaignAPI) ArchiveCampaign(ctx context.Context, req *pb.ArchiveCampaignRequest) (*pb.Campaign, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	c, err := a.svc.Archive(ctx, req.Id)
	if err != nil {
		return nil, mapCampaignErr(err)
	}
	return a.toProto(c), nil
}

// ─── GetCampaign ───────────────────────────────────────────────────

func (a *CampaignAPI) GetCampaign(ctx context.Context, req *pb.GetCampaignRequest) (*pb.CampaignWithStats, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	from, _ := parseRFC3339Optional(req.From)
	to, _ := parseRFC3339Optional(req.To)
	c, st, err := a.svc.Get(ctx, req.Id, from, to)
	if err != nil {
		return nil, mapCampaignErr(err)
	}
	return &pb.CampaignWithStats{
		Campaign: a.toProto(c),
		Stats:    statsToProto(st),
	}, nil
}

// ─── ListCampaigns ─────────────────────────────────────────────────

func (a *CampaignAPI) ListCampaigns(ctx context.Context, req *pb.ListCampaignsRequest) (*pb.ListCampaignsResponse, error) {
	items, statsList, total, err := a.svc.List(ctx, req.IncludeArchived, req.Limit, req.Offset)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to list campaigns")
	}
	out := make([]*pb.CampaignWithStats, 0, len(items))
	for i, c := range items {
		out = append(out, &pb.CampaignWithStats{
			Campaign: a.toProto(c),
			Stats:    statsToProto(statsList[i]),
		})
	}
	return &pb.ListCampaignsResponse{Campaigns: out, Total: total}, nil
}

// ─── GetCampaignStats ──────────────────────────────────────────────

func (a *CampaignAPI) GetCampaignStats(ctx context.Context, req *pb.GetCampaignStatsRequest) (*pb.CampaignStats, error) {
	if req.CampaignId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "campaign_id is required")
	}
	from, _ := parseRFC3339Optional(req.From)
	to, _ := parseRFC3339Optional(req.To)
	st, err := a.svc.GetStats(ctx, req.CampaignId, from, to)
	if err != nil {
		return nil, mapCampaignErr(err)
	}
	return statsToProto(st), nil
}

// ─── helpers ────────────────────────────────────────────────────────

func (a *CampaignAPI) toProto(c *model.Campaign) *pb.Campaign {
	out := &pb.Campaign{
		Id:        c.ID,
		Slug:      c.Slug,
		Name:      c.Name,
		Notes:     c.Notes,
		IsActive:  c.IsActive,
		CreatedBy: c.CreatedBy,
		CreatedAt: c.CreatedAt.Format(time.RFC3339),
		DeepLink:  a.svc.BuildDeepLink(c.Slug),
	}
	if c.PartnerUserID != nil {
		out.PartnerUserId = *c.PartnerUserID
	}
	if c.PayoutPercent != nil {
		out.PayoutPercent = *c.PayoutPercent
	}
	if c.ArchivedAt != nil {
		out.ArchivedAt = c.ArchivedAt.Format(time.RFC3339)
	}
	if c.TrialDurationDays != nil {
		out.TrialDurationDays = *c.TrialDurationDays
	}
	return out
}

func statsToProto(st *model.CampaignStats) *pb.CampaignStats {
	out := &pb.CampaignStats{
		CampaignId:        st.CampaignID,
		Starts:            st.Starts,
		OpenedApp:         st.OpenedApp,
		TrialActivated:    st.TrialActivated,
		PaidUsers:         st.PaidUsers,
		RevenueRub:        fmt.Sprintf("%.2f", st.RevenueRUB),
		PartnerPayoutsRub: fmt.Sprintf("%.2f", st.PartnerPayoutsRUB),
	}
	if !st.From.IsZero() {
		out.From = st.From.Format(time.RFC3339)
	}
	if !st.To.IsZero() {
		out.To = st.To.Format(time.RFC3339)
	}
	return out
}

func parseRFC3339Optional(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, s)
}

// mapCampaignErr — единый маппинг доменных ошибок в gRPC-коды.
func mapCampaignErr(err error) error {
	switch {
	case errors.Is(err, repository.ErrCampaignNotFound):
		return status.Error(codes.NotFound, "campaign not found")
	case errors.Is(err, repository.ErrCampaignSlugExists):
		return status.Error(codes.AlreadyExists, "campaign slug already exists")
	case errors.Is(err, service.ErrInvalidSlug),
		errors.Is(err, service.ErrInvalidPayoutPercent),
		errors.Is(err, service.ErrPayoutWithoutPartner),
		errors.Is(err, service.ErrInvalidTrialDuration):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Errorf(codes.Internal, "%v", err)
	}
}
