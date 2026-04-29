package api

import (
	"context"
	"fmt"

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
