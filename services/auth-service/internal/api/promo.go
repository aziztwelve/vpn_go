// Package api — promo.go: gRPC ручки персональных промо-токенов.
//
// Вызываются gateway'ем:
//   - bot-команда `/promo @username` → IssuePromo
//   - HTTP GET /promo/p/{token} → RedeemPromo + AttachPayment
//   - bot-команда `/promo_status @username` → GetPromoStatus
//   - payment-service paid-webhook → MarkUsed
//
// Авторизация: Auth.admin_telegram_id (для bot-команд) ИЛИ
// Auth.admin_user_id (для HTTP-ручек) — ровно один. Те же правила, что
// и в BroadcastAPI; reuse PromoAPI.authorize() через shared helper.
//
// RedeemPromo и MarkUsed — БЕЗ авторизации (вызываются публичным GET и
// payment-webhook соответственно).
package api

import (
	"context"
	"errors"
	"time"

	"github.com/vpn/auth-service/internal/repository"
	pb "github.com/vpn/shared/pkg/proto/promo/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DefaultPromoTTL — 30 дней. Если не задано в IssuePromoRequest.ttl_seconds,
// промо живёт месяц с момента выдачи.
const DefaultPromoTTL = 30 * 24 * time.Hour

// PromoAPI — gRPC handler. Использует BroadcastRepository.IsAdmin*
// для проверки админ-роли (общая БД, общая логика — DRY).
type PromoAPI struct {
	pb.UnimplementedPromoServiceServer
	repo      *repository.PromoRepository
	adminRepo *repository.BroadcastRepository // только для IsAdmin/IsAdminByUserID
	logger    *zap.Logger
}

func NewPromoAPI(
	repo *repository.PromoRepository,
	adminRepo *repository.BroadcastRepository,
	logger *zap.Logger,
) *PromoAPI {
	return &PromoAPI{repo: repo, adminRepo: adminRepo, logger: logger}
}

// authorize резолвит Auth → users.id и проверяет role='admin'.
// Зеркалит broadcast.go.authorize() (та же логика, разные сервисы).
func (a *PromoAPI) authorize(ctx context.Context, auth *pb.Auth) (int64, error) {
	if auth == nil {
		return 0, status.Error(codes.InvalidArgument, "auth required")
	}
	if auth.AdminUserId == 0 && auth.AdminTelegramId == 0 {
		return 0, status.Error(codes.InvalidArgument,
			"admin_telegram_id or admin_user_id required")
	}

	if auth.AdminUserId != 0 {
		ok, err := a.adminRepo.IsAdminByUserID(ctx, auth.AdminUserId)
		if err != nil {
			return 0, status.Error(codes.PermissionDenied, "admin not found")
		}
		if !ok {
			return 0, status.Error(codes.PermissionDenied, "admin role required")
		}
		return auth.AdminUserId, nil
	}

	ok, userID, err := a.adminRepo.IsAdmin(ctx, auth.AdminTelegramId)
	if err != nil {
		return 0, status.Error(codes.PermissionDenied, "admin not found")
	}
	if !ok {
		return 0, status.Error(codes.PermissionDenied, "admin role required")
	}
	return userID, nil
}

// IssuePromo — выдаёт новый токен или возвращает существующий активный.
// Admin-only.
func (a *PromoAPI) IssuePromo(
	ctx context.Context,
	req *pb.IssuePromoRequest,
) (*pb.IssuePromoResponse, error) {
	if req.UserId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id required")
	}
	if req.PlanId == 0 {
		return nil, status.Error(codes.InvalidArgument, "plan_id required")
	}
	adminUserID, err := a.authorize(ctx, req.Auth)
	if err != nil {
		return nil, err
	}

	ttl := DefaultPromoTTL
	if req.TtlSeconds > 0 {
		ttl = time.Duration(req.TtlSeconds) * time.Second
	}

	maxDevices := req.MaxDevices
	if maxDevices == 0 {
		maxDevices = 2
	}

	code, alreadyExisted, err := a.repo.Issue(ctx, repository.IssueInput{
		UserID:     req.UserId,
		PlanID:     req.PlanId,
		MaxDevices: maxDevices,
		CreatedBy:  adminUserID,
		TTL:        ttl,
	})
	if err != nil {
		a.logger.Error("IssuePromo: failed",
			zap.Int64("user_id", req.UserId),
			zap.Int32("plan_id", req.PlanId),
			zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to issue promo")
	}

	a.logger.Info("IssuePromo: ok",
		zap.Int64("promo_id", code.ID),
		zap.Int64("user_id", code.UserID),
		zap.Int32("plan_id", code.PlanID),
		zap.Bool("already_existed", alreadyExisted),
		zap.Int64("admin_user_id", adminUserID),
	)

	resp := &pb.IssuePromoResponse{
		Token:          code.Token,
		PromoId:        code.ID,
		AlreadyExisted: alreadyExisted,
	}
	if code.ExpiresAt != nil {
		resp.ExpiresAtUnix = code.ExpiresAt.Unix()
	}
	return resp, nil
}

// RedeemPromo — публичный (без auth). Возвращает данные для CreateInvoice.
func (a *PromoAPI) RedeemPromo(
	ctx context.Context,
	req *pb.RedeemPromoRequest,
) (*pb.RedeemPromoResponse, error) {
	if req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "token required")
	}

	code, err := a.repo.GetByToken(ctx, req.Token)
	if errors.Is(err, repository.ErrPromoNotFound) {
		return nil, status.Error(codes.NotFound, "promo not found")
	}
	if err != nil {
		a.logger.Error("RedeemPromo: lookup failed",
			zap.String("token_prefix", safeTokenPrefix(req.Token)),
			zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to lookup promo")
	}

	if code.IsExpired() {
		return nil, status.Error(codes.FailedPrecondition, "promo expired")
	}

	resp := &pb.RedeemPromoResponse{
		PromoId:    code.ID,
		UserId:     code.UserID,
		PlanId:     code.PlanID,
		MaxDevices: code.MaxDevices,
	}
	if code.PaymentID != nil {
		resp.ExistingPaymentId = *code.PaymentID
	}
	if code.UsedAt != nil {
		resp.AlreadyUsed = true
	}
	return resp, nil
}

// AttachPayment — после CreateInvoice прикрепить payment_id к токену.
// Без auth (вызывается gateway'ем сразу за RedeemPromo).
func (a *PromoAPI) AttachPayment(
	ctx context.Context,
	req *pb.AttachPaymentRequest,
) (*pb.AttachPaymentResponse, error) {
	if req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "token required")
	}
	if req.PaymentId == 0 {
		return nil, status.Error(codes.InvalidArgument, "payment_id required")
	}

	if err := a.repo.AttachPayment(ctx, req.Token, req.PaymentId); err != nil {
		if errors.Is(err, repository.ErrPromoNotFound) {
			return nil, status.Error(codes.NotFound, "promo not found")
		}
		a.logger.Error("AttachPayment: failed",
			zap.String("token_prefix", safeTokenPrefix(req.Token)),
			zap.Int64("payment_id", req.PaymentId),
			zap.Error(err))
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &pb.AttachPaymentResponse{Ok: true}, nil
}

// MarkUsed — вызывается payment-service по paid-webhook'у. Без auth.
// matched=false если payment не связан с promo (обычная оплата).
func (a *PromoAPI) MarkUsed(
	ctx context.Context,
	req *pb.MarkUsedRequest,
) (*pb.MarkUsedResponse, error) {
	if req.PaymentId == 0 {
		return nil, status.Error(codes.InvalidArgument, "payment_id required")
	}

	matched, promoID, userID, err := a.repo.MarkUsedByPayment(ctx, req.PaymentId)
	if err != nil {
		a.logger.Error("MarkUsed: failed",
			zap.Int64("payment_id", req.PaymentId), zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to mark used")
	}

	if matched {
		a.logger.Info("MarkUsed: matched",
			zap.Int64("promo_id", promoID),
			zap.Int64("user_id", userID),
			zap.Int64("payment_id", req.PaymentId))
	}

	return &pb.MarkUsedResponse{
		Matched: matched,
		PromoId: promoID,
		UserId:  userID,
	}, nil
}

// GetPromoStatus — для админ-команды /promo_status. Возвращает 0-val поля
// для not-set состояний (used_at_unix=0 если не использован, payment_id=0
// если не было кликов).
func (a *PromoAPI) GetPromoStatus(
	ctx context.Context,
	req *pb.GetPromoStatusRequest,
) (*pb.GetPromoStatusResponse, error) {
	if req.UserId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id required")
	}
	if req.PlanId == 0 {
		return nil, status.Error(codes.InvalidArgument, "plan_id required")
	}
	if _, err := a.authorize(ctx, req.Auth); err != nil {
		return nil, err
	}

	// Сначала ищем активный, если нет — ищем самый свежий used.
	code, err := a.repo.GetActiveByUserPlan(ctx, req.UserId, req.PlanId)
	if errors.Is(err, repository.ErrPromoNotFound) {
		// Можно расширить repo `GetLatestByUserPlan` чтобы и used'ы видеть,
		// но для MVP "нет активного" = NotFound.
		return nil, status.Error(codes.NotFound, "no active promo")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to get status")
	}

	resp := &pb.GetPromoStatusResponse{
		PromoId:       code.ID,
		Token:         code.Token,
		CreatedAtUnix: code.CreatedAt.Unix(),
	}
	if code.ExpiresAt != nil {
		resp.ExpiresAtUnix = code.ExpiresAt.Unix()
	}
	if code.UsedAt != nil {
		resp.UsedAtUnix = code.UsedAt.Unix()
	}
	if code.PaymentID != nil {
		resp.PaymentId = *code.PaymentID
	}
	return resp, nil
}

// LookupUser — резолв @username/telegram_id → user.id для админ-команд.
// Admin-only: чтобы не палить чужие user_id обычным юзерам.
func (a *PromoAPI) LookupUser(
	ctx context.Context,
	req *pb.LookupUserRequest,
) (*pb.LookupUserResponse, error) {
	if _, err := a.authorize(ctx, req.Auth); err != nil {
		return nil, err
	}
	if req.Username == "" && req.TelegramId == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"username or telegram_id required")
	}

	// LookupOrCreate*: если в users нет, fallback на bot_starts (юзер
	// нажал /start, но не открыл Mini App) с авто-созданием shadow-юзера.
	// Нужно для админ-рассылки /promo по холодным bounced-юзерам.
	var (
		u       *repository.UserLookup
		created bool
		err     error
	)
	if req.TelegramId != 0 {
		u, created, err = a.repo.LookupOrCreateByTelegramID(ctx, req.TelegramId)
	} else {
		// Снимаем @-префикс если случайно прилетел.
		uname := req.Username
		if len(uname) > 0 && uname[0] == '@' {
			uname = uname[1:]
		}
		u, created, err = a.repo.LookupOrCreateByUsername(ctx, uname)
	}
	if errors.Is(err, repository.ErrPromoNotFound) {
		return nil, status.Error(codes.NotFound, "user not found")
	}
	if err != nil {
		a.logger.Error("LookupUser: failed",
			zap.String("username", req.Username),
			zap.Int64("telegram_id", req.TelegramId),
			zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to lookup")
	}
	if created {
		a.logger.Info("LookupUser: shadow user created from bot_starts",
			zap.Int64("user_id", u.ID),
			zap.Int64("telegram_id", u.TelegramID),
			zap.String("username", u.Username),
		)
	}

	return &pb.LookupUserResponse{
		UserId:     u.ID,
		TelegramId: u.TelegramID,
		Username:   u.Username,
		FirstName:  u.FirstName,
	}, nil
}

// safeTokenPrefix — первые 8 символов токена для логов (не палим полный).
func safeTokenPrefix(t string) string {
	if len(t) < 8 {
		return t
	}
	return t[:8] + "…"
}
