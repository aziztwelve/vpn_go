package api

import (
	"context"
	"errors"

	"github.com/vpn/payment-service/internal/model"
	"github.com/vpn/payment-service/internal/repository"
	"github.com/vpn/payment-service/internal/service"
	pb "github.com/vpn/shared/pkg/proto/payment/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type PaymentAPI struct {
	pb.UnimplementedPaymentServiceServer
	svc *service.PaymentService
	log *zap.Logger
}

func New(svc *service.PaymentService, log *zap.Logger) *PaymentAPI {
	return &PaymentAPI{svc: svc, log: log}
}

func (a *PaymentAPI) CreateInvoice(ctx context.Context, req *pb.CreateInvoiceRequest) (*pb.CreateInvoiceResponse, error) {
	if req.GetUserId() == 0 || req.GetPlanId() == 0 || req.GetMaxDevices() == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id, plan_id, max_devices required")
	}
	p, link, err := a.svc.CreateInvoice(ctx, req.GetUserId(), req.GetPlanId(), req.GetMaxDevices())
	if err != nil {
		if errors.Is(err, service.ErrInvalidPlan) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		a.log.Error("CreateInvoice failed", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to create invoice")
	}
	return &pb.CreateInvoiceResponse{
		PaymentId:   p.ID,
		InvoiceLink: link,
		AmountStars: p.AmountStars,
	}, nil
}

func (a *PaymentAPI) GetPayment(ctx context.Context, req *pb.GetPaymentRequest) (*pb.GetPaymentResponse, error) {
	if req.GetPaymentId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "payment_id required")
	}
	p, err := a.svc.GetPayment(ctx, req.GetPaymentId())
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "payment not found")
		}
		return nil, status.Error(codes.Internal, "failed to get payment")
	}
	return &pb.GetPaymentResponse{Payment: toPB(p)}, nil
}

func (a *PaymentAPI) ListUserPayments(ctx context.Context, req *pb.ListUserPaymentsRequest) (*pb.ListUserPaymentsResponse, error) {
	if req.GetUserId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id required")
	}
	list, err := a.svc.ListByUser(ctx, req.GetUserId(), req.GetLimit(), req.GetOffset())
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to list payments")
	}
	out := make([]*pb.Payment, 0, len(list))
	for _, p := range list {
		out = append(out, toPB(p))
	}
	return &pb.ListUserPaymentsResponse{Payments: out}, nil
}

func (a *PaymentAPI) HandleTelegramUpdate(ctx context.Context, req *pb.HandleTelegramUpdateRequest) (*pb.HandleTelegramUpdateResponse, error) {
	if len(req.GetUpdateJson()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "update_json required")
	}
	action, err := a.svc.HandleUpdate(ctx, req.GetUpdateJson())
	if err != nil {
		a.log.Error("HandleUpdate failed", zap.String("action", action), zap.Error(err))
		// Важно: возвращаем 200 для "duplicate" / "ignored" чтобы Telegram не ретраил.
		// Только при реальных серверных ошибках — 5xx (чтобы Telegram повторил).
		return nil, status.Error(codes.Internal, "update handler failed: "+err.Error())
	}
	return &pb.HandleTelegramUpdateResponse{Handled: true, Action: action}, nil
}

func toPB(p *model.Payment) *pb.Payment {
	out := &pb.Payment{
		Id:          p.ID,
		UserId:      p.UserID,
		PlanId:      p.PlanID,
		MaxDevices:  p.MaxDevices,
		AmountStars: p.AmountStars,
		Status:      p.Status,
		Provider:    p.Provider,
		CreatedAt:   p.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if p.ExternalID != nil {
		out.ExternalId = *p.ExternalID
	}
	if p.PaidAt != nil {
		out.PaidAt = p.PaidAt.Format("2006-01-02T15:04:05Z")
	}
	return out
}
