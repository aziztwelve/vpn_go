package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/vpn/auth-service/internal/model"
	"github.com/vpn/auth-service/internal/service"
	pb "github.com/vpn/shared/pkg/proto/auth/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type AuthAPI struct {
	pb.UnimplementedAuthServiceServer
	authService *service.AuthService
	logger      *zap.Logger
}

func NewAuthAPI(authService *service.AuthService, logger *zap.Logger) *AuthAPI {
	return &AuthAPI{
		authService: authService,
		logger:      logger,
	}
}

func (a *AuthAPI) ValidateTelegramUser(ctx context.Context, req *pb.ValidateTelegramUserRequest) (*pb.ValidateTelegramUserResponse, error) {
	if req.InitData == "" {
		return nil, status.Error(codes.InvalidArgument, "init_data is required")
	}

	res, err := a.authService.ValidateTelegramUser(ctx, req.InitData, req.RefToken)
	if err != nil {
		a.logger.Error("Failed to validate telegram user", zap.Error(err))
		return nil, status.Error(codes.Unauthenticated, "invalid init data")
	}

	return &pb.ValidateTelegramUserResponse{
		User:               modelUserToProto(res.User),
		JwtToken:           res.JWTToken,
		IsNewUser:          res.IsNewUser,
		ReferralRegistered: res.ReferralRegistered,
	}, nil
}

func (a *AuthAPI) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.GetUserResponse, error) {
	if req.UserId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	user, err := a.authService.GetUser(ctx, req.UserId)
	if err != nil {
		a.logger.Error("Failed to get user", zap.Error(err))
		return nil, status.Error(codes.NotFound, "user not found")
	}

	return &pb.GetUserResponse{
		User: modelUserToProto(user),
	}, nil
}

// GetUserByTelegramID — gateway-handler'ы бота знают telegram_id из webhook'а,
// но subscription/payment-сервисам нужен внутренний users.id. Маппим pgx.ErrNoRows
// в codes.NotFound, чтобы вызывающая сторона могла отличить «юзер не зарегистрирован»
// (ожидаемо для бот-команд до открытия Mini App) от настоящих ошибок.
func (a *AuthAPI) GetUserByTelegramID(ctx context.Context, req *pb.GetUserByTelegramIDRequest) (*pb.GetUserByTelegramIDResponse, error) {
	if req.TelegramId == 0 {
		return nil, status.Error(codes.InvalidArgument, "telegram_id is required")
	}

	user, err := a.authService.GetUserByTelegramID(ctx, req.TelegramId)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "user not found")
		}
		a.logger.Error("Failed to get user by telegram_id", zap.Int64("telegram_id", req.TelegramId), zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to get user")
	}

	return &pb.GetUserByTelegramIDResponse{
		User: modelUserToProto(user),
	}, nil
}

func (a *AuthAPI) UpdateUserRole(ctx context.Context, req *pb.UpdateUserRoleRequest) (*pb.UpdateUserRoleResponse, error) {
	if req.UserId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}
	if req.Role == "" {
		return nil, status.Error(codes.InvalidArgument, "role is required")
	}

	// Валидация роли
	if req.Role != model.RoleUser && req.Role != model.RolePartner && req.Role != model.RoleAdmin {
		return nil, status.Error(codes.InvalidArgument, "invalid role")
	}

	user, err := a.authService.UpdateUserRole(ctx, req.UserId, req.Role)
	if err != nil {
		a.logger.Error("Failed to update user role", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to update role")
	}

	return &pb.UpdateUserRoleResponse{
		User: modelUserToProto(user),
	}, nil
}

// SelfUpdateRole — self-service смена своей роли. Принимает user_id из
// JWT (gateway проставляет) и role ∈ {user, partner}. admin запрещён.
// Возвращает свежий JWT с обновлённой ролью.
func (a *AuthAPI) SelfUpdateRole(ctx context.Context, req *pb.SelfUpdateRoleRequest) (*pb.SelfUpdateRoleResponse, error) {
	if req.UserId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}
	if req.Role != model.RoleUser && req.Role != model.RolePartner {
		return nil, status.Error(codes.InvalidArgument, "role must be 'user' or 'partner'")
	}

	user, token, err := a.authService.SelfUpdateRole(ctx, req.UserId, req.Role)
	if err != nil {
		a.logger.Error("Failed to self-update role", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to update role")
	}

	a.logger.Info("user role self-updated",
		zap.Int64("user_id", user.ID),
		zap.String("role", user.Role),
	)

	return &pb.SelfUpdateRoleResponse{
		User:     modelUserToProto(user),
		JwtToken: token,
	}, nil
}

func (a *AuthAPI) BanUser(ctx context.Context, req *pb.BanUserRequest) (*pb.BanUserResponse, error) {
	if req.UserId == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	user, err := a.authService.BanUser(ctx, req.UserId, req.IsBanned)
	if err != nil {
		a.logger.Error("Failed to ban user", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to ban user")
	}

	return &pb.BanUserResponse{
		User: modelUserToProto(user),
	}, nil
}

// SetPendingReferral — bot-side вход для /start ref_<token>. Вызывает
// gateway, когда пользователь кликает реф-ссылку и попадает в чат
// бота ДО регистрации в Mini App. ValidateTelegramUser потом "съест"
// эту запись на первой регистрации.
func (a *AuthAPI) SetPendingReferral(ctx context.Context, req *pb.SetPendingReferralRequest) (*pb.SetPendingReferralResponse, error) {
	if req.TelegramId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "telegram_id is required")
	}
	if req.RefToken == "" {
		return nil, status.Error(codes.InvalidArgument, "ref_token is required")
	}
	if err := a.authService.SetPendingReferral(ctx, req.TelegramId, req.RefToken); err != nil {
		a.logger.Error("Failed to set pending referral",
			zap.Int64("telegram_id", req.TelegramId),
			zap.String("ref_token", req.RefToken),
			zap.Error(err),
		)
		return nil, status.Error(codes.Internal, "failed to store pending referral")
	}
	a.logger.Info("pending referral stored",
		zap.Int64("telegram_id", req.TelegramId),
		zap.String("ref_token", req.RefToken),
	)
	return &pb.SetPendingReferralResponse{Stored: true}, nil
}

// RecordBotStart — записываем нажатие /start в боте (воронка бот → Mini App).
// Idempotent через ON CONFLICT DO NOTHING: stored=false на повторных нажатиях.
//
// Если start_param = "src_<slug>" и кампания активна — campaign_id будет
// заполнен в bot_starts и вернётся в ответе (для аналитики).
func (a *AuthAPI) RecordBotStart(ctx context.Context, req *pb.RecordBotStartRequest) (*pb.RecordBotStartResponse, error) {
	if req.TelegramId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "telegram_id is required")
	}
	stored, campaignID, err := a.authService.RecordBotStart(ctx, req.TelegramId, req.Username, req.FirstName, req.StartParam)
	if err != nil {
		a.logger.Error("Failed to record bot start",
			zap.Int64("telegram_id", req.TelegramId),
			zap.Error(err),
		)
		return nil, status.Error(codes.Internal, "failed to record bot start")
	}
	if stored {
		a.logger.Info("bot /start recorded",
			zap.Int64("telegram_id", req.TelegramId),
			zap.String("username", req.Username),
			zap.String("start_param", req.StartParam),
			zap.Int64("campaign_id", campaignID),
		)
	}
	return &pb.RecordBotStartResponse{Stored: stored, CampaignId: campaignID}, nil
}

// SetPendingCampaign — bot-side вход для /start src_<slug>. Вызывается когда
// юзер кликает deep-link воронки и попадает в бот ДО открытия Mini App.
// ValidateTelegramUser потом "съест" эту запись при первой регистрации и
// положит её в user_attribution (first-touch, навсегда).
//
// Если slug не найден / кампания архивирована → stored=false, campaign_id=0
// (без ошибки, это ожидаемый кейс старых ссылок).
func (a *AuthAPI) SetPendingCampaign(ctx context.Context, req *pb.SetPendingCampaignRequest) (*pb.SetPendingCampaignResponse, error) {
	if req.TelegramId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "telegram_id is required")
	}
	if req.Slug == "" {
		return nil, status.Error(codes.InvalidArgument, "slug is required")
	}
	campaignID, err := a.authService.SetPendingCampaign(ctx, req.TelegramId, req.Slug)
	if err != nil {
		a.logger.Error("Failed to set pending campaign",
			zap.Int64("telegram_id", req.TelegramId),
			zap.String("slug", req.Slug),
			zap.Error(err),
		)
		return nil, status.Error(codes.Internal, "failed to set pending campaign")
	}
	if campaignID == 0 {
		// slug не найден или кампания архивирована — не ошибка.
		return &pb.SetPendingCampaignResponse{Stored: false}, nil
	}
	a.logger.Info("pending campaign set",
		zap.Int64("telegram_id", req.TelegramId),
		zap.String("slug", req.Slug),
		zap.Int64("campaign_id", campaignID),
	)
	return &pb.SetPendingCampaignResponse{Stored: true, CampaignId: campaignID}, nil
}

// RegisterFromBot — full-init юзера прямо из webhook'а бота при /start.
// Аналог ValidateTelegramUser, но без initData/JWT (бот их не имеет/не нужен).
//
// Идемпотентен: повторные /start от того же telegram_id не создают дубликатов.
// Атрибуция (ref/campaign) делается синхронно и только для НОВЫХ юзеров.
func (a *AuthAPI) RegisterFromBot(ctx context.Context, req *pb.RegisterFromBotRequest) (*pb.RegisterFromBotResponse, error) {
	if req.TelegramId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "telegram_id is required")
	}
	res, err := a.authService.RegisterFromBot(
		ctx,
		req.TelegramId,
		req.Username,
		req.FirstName,
		req.LastName,
		req.LanguageCode,
		req.StartParam,
	)
	if err != nil {
		a.logger.Error("RegisterFromBot failed",
			zap.Int64("telegram_id", req.TelegramId), zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to register from bot")
	}
	return &pb.RegisterFromBotResponse{
		User:                 modelUserToProto(res.User),
		IsNewUser:            res.IsNewUser,
		ReferralRegistered:   res.ReferralRegistered,
		AttributedCampaignId: res.AttributedCampaignID,
	}, nil
}

func (a *AuthAPI) VerifyToken(ctx context.Context, req *pb.VerifyTokenRequest) (*pb.VerifyTokenResponse, error) {
	if req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}

	userID, role, err := a.authService.VerifyJWT(req.Token)
	if err != nil {
		return &pb.VerifyTokenResponse{
			IsValid: false,
		}, nil
	}

	return &pb.VerifyTokenResponse{
		UserId:  userID,
		Role:    role,
		IsValid: true,
	}, nil
}

func modelUserToProto(user *model.User) *pb.User {
	var lastActiveAt string
	if user.LastActiveAt != nil {
		lastActiveAt = user.LastActiveAt.Format("2006-01-02T15:04:05Z")
	}

	return &pb.User{
		Id:           user.ID,
		TelegramId:   user.TelegramID,
		Username:     user.Username,
		FirstName:    user.FirstName,
		LastName:     user.LastName,
		PhotoUrl:     user.PhotoURL,
		LanguageCode: user.LanguageCode,
		Role:         user.Role,
		IsBanned:     user.IsBanned,
		Balance:      fmt.Sprintf("%.2f", user.Balance),
		CreatedAt:    user.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:    user.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		LastActiveAt: lastActiveAt,
	}
}
