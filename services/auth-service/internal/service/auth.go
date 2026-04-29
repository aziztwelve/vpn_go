package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/vpn/auth-service/internal/model"
	"github.com/vpn/auth-service/internal/repository"
	referralpb "github.com/vpn/shared/pkg/proto/referral/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// ReferralClient — узкий интерфейс к referral-service. Может быть nil
// (referral не задеплоен или адрес не настроен) — тогда вся реферальная
// логика молча пропускается.
type ReferralClient interface {
	RegisterReferral(ctx context.Context, req *referralpb.RegisterReferralRequest, opts ...grpc.CallOption) (*referralpb.RegisterReferralResponse, error)
}

type AuthService struct {
	userRepo      *repository.UserRepository
	jwtSecret     string
	jwtTTLHours   int
	telegramToken string
	referral      ReferralClient // может быть nil
	logger        *zap.Logger
}

func NewAuthService(userRepo *repository.UserRepository, jwtSecret string, jwtTTLHours int, telegramToken string, referral ReferralClient, logger *zap.Logger) *AuthService {
	return &AuthService{
		userRepo:      userRepo,
		jwtSecret:     jwtSecret,
		jwtTTLHours:   jwtTTLHours,
		telegramToken: telegramToken,
		referral:      referral,
		logger:        logger,
	}
}

// ValidateTelegramInitData проверяет подпись Telegram initData
func (s *AuthService) ValidateTelegramInitData(initData string) (map[string]string, error) {
	values, err := url.ParseQuery(initData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse init data: %w", err)
	}

	dataMap := make(map[string]string)
	for k, v := range values {
		if len(v) > 0 {
			dataMap[k] = v[0]
		}
	}

	hash, ok := dataMap["hash"]
	if !ok {
		return nil, fmt.Errorf("hash not found in init data")
	}

	// Создаём data_check_string
	var keys []string
	for k := range dataMap {
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var dataCheckString strings.Builder
	for i, k := range keys {
		if i > 0 {
			dataCheckString.WriteString("\n")
		}
		dataCheckString.WriteString(k)
		dataCheckString.WriteString("=")
		dataCheckString.WriteString(dataMap[k])
	}

	// Вычисляем secret_key
	secretKey := hmac.New(sha256.New, []byte("WebAppData"))
	secretKey.Write([]byte(s.telegramToken))

	// Вычисляем hash
	h := hmac.New(sha256.New, secretKey.Sum(nil))
	h.Write([]byte(dataCheckString.String()))
	expectedHash := hex.EncodeToString(h.Sum(nil))

	if hash != expectedHash {
		return nil, fmt.Errorf("invalid hash")
	}

	// Проверяем auth_date (не старше 24 часов)
	authDateStr, ok := dataMap["auth_date"]
	if !ok {
		return nil, fmt.Errorf("auth_date not found")
	}

	authDate, err := strconv.ParseInt(authDateStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid auth_date: %w", err)
	}

	if time.Now().Unix()-authDate > 86400 {
		return nil, fmt.Errorf("init data expired")
	}

	return dataMap, nil
}

// ValidateResult — расширенный результат валидации с реферальной информацией.
type ValidateResult struct {
	User               *model.User
	JWTToken           string
	IsNewUser          bool
	ReferralRegistered bool
}

// ValidateTelegramUser валидирует и создаёт/обновляет пользователя.
// Если refToken не пуст и юзер был только что создан — параллельно
// регистрирует реферал (best-effort, ошибки не блокируют auth).
func (s *AuthService) ValidateTelegramUser(ctx context.Context, initData, refToken string) (*ValidateResult, error) {
	dataMap, err := s.ValidateTelegramInitData(initData)
	if err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	// Парсим user из JSON
	userJSON, ok := dataMap["user"]
	if !ok {
		return nil, fmt.Errorf("user not found in init data")
	}

	// Простой парсинг JSON (можно использовать encoding/json для более сложных случаев)
	var telegramID int64
	var username, firstName, lastName, photoURL, langCode string

	// Парсим telegram_id
	if strings.Contains(userJSON, `"id":`) {
		parts := strings.Split(userJSON, `"id":`)
		if len(parts) > 1 {
			idStr := strings.Split(parts[1], ",")[0]
			idStr = strings.TrimSpace(idStr)
			telegramID, _ = strconv.ParseInt(idStr, 10, 64)
		}
	}

	// Парсим username
	if strings.Contains(userJSON, `"username":"`) {
		parts := strings.Split(userJSON, `"username":"`)
		if len(parts) > 1 {
			username = strings.Split(parts[1], `"`)[0]
		}
	}

	// Парсим first_name
	if strings.Contains(userJSON, `"first_name":"`) {
		parts := strings.Split(userJSON, `"first_name":"`)
		if len(parts) > 1 {
			firstName = strings.Split(parts[1], `"`)[0]
		}
	}

	// Парсим last_name
	if strings.Contains(userJSON, `"last_name":"`) {
		parts := strings.Split(userJSON, `"last_name":"`)
		if len(parts) > 1 {
			lastName = strings.Split(parts[1], `"`)[0]
		}
	}

	// Парсим language_code
	if strings.Contains(userJSON, `"language_code":"`) {
		parts := strings.Split(userJSON, `"language_code":"`)
		if len(parts) > 1 {
			langCode = strings.Split(parts[1], `"`)[0]
		}
	}

	if telegramID == 0 {
		return nil, fmt.Errorf("invalid telegram_id")
	}

	// Проверяем существует ли пользователь
	isNewUser := false
	user, err := s.userRepo.GetUserByTelegramID(ctx, telegramID)
	if err != nil {
		// Пользователь не найден, создаём нового
		user, err = s.userRepo.CreateUser(ctx, telegramID, username, firstName, lastName, photoURL, langCode)
		if err != nil {
			return nil, fmt.Errorf("failed to create user: %w", err)
		}
		isNewUser = true
		s.logger.Info("New user created", zap.Int64("telegram_id", telegramID))
	} else {
		// Обновляем данные пользователя
		user.Username = username
		user.FirstName = firstName
		user.LastName = lastName
		user.PhotoURL = photoURL
		user.LanguageCode = langCode
		if err := s.userRepo.UpdateUser(ctx, user); err != nil {
			s.logger.Warn("Failed to update user", zap.Error(err))
		}
	}

	// Обновляем last_active_at
	_ = s.userRepo.UpdateLastActive(ctx, user.ID)

	// Генерируем JWT токен
	token, err := s.GenerateJWT(user.ID, user.Role)
	if err != nil {
		return nil, fmt.Errorf("failed to generate JWT: %w", err)
	}

	// Реферальная регистрация — только для новых юзеров и только если есть
	// токен и сконфигурирован клиент. Ошибки игнорируем (best-effort).
	referralRegistered := false
	if isNewUser && refToken != "" && s.referral != nil {
		referralRegistered = s.tryRegisterReferral(ctx, refToken, user.ID)
	}

	return &ValidateResult{
		User:               user,
		JWTToken:           token,
		IsNewUser:          isNewUser,
		ReferralRegistered: referralRegistered,
	}, nil
}

// tryRegisterReferral — best-effort вызов referral-service. Никогда не
// блокирует и не падает — auth важнее реферала.
func (s *AuthService) tryRegisterReferral(ctx context.Context, refToken string, invitedID int64) bool {
	resp, err := s.referral.RegisterReferral(ctx, &referralpb.RegisterReferralRequest{
		InviterToken:   refToken,
		InvitedUserId:  invitedID,
	})
	if err != nil {
		s.logger.Warn("referral.RegisterReferral failed (non-blocking)",
			zap.String("ref_token", refToken),
			zap.Int64("invited_id", invitedID),
			zap.Error(err),
		)
		return false
	}
	if !resp.Registered {
		s.logger.Info("referral registration skipped",
			zap.String("ref_token", refToken),
			zap.Int64("invited_id", invitedID),
			zap.String("reason", resp.SkipReason),
		)
		return false
	}
	s.logger.Info("referral registered",
		zap.String("ref_token", refToken),
		zap.Int64("invited_id", invitedID),
		zap.Int64("inviter_id", resp.InviterUserId),
	)
	return true
}

// GenerateJWT генерирует JWT токен
func (s *AuthService) GenerateJWT(userID int64, role string) (string, error) {
	claims := jwt.MapClaims{
		"user_id": userID,
		"role":    role,
		"exp":     time.Now().Add(time.Hour * time.Duration(s.jwtTTLHours)).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.jwtSecret))
}

// VerifyJWT проверяет JWT токен
func (s *AuthService) VerifyJWT(tokenString string) (int64, string, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(s.jwtSecret), nil
	})

	if err != nil {
		return 0, "", err
	}

	if !token.Valid {
		return 0, "", fmt.Errorf("invalid token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return 0, "", fmt.Errorf("invalid claims")
	}

	userID := int64(claims["user_id"].(float64))
	role := claims["role"].(string)

	return userID, role, nil
}

// GetUser получает пользователя по ID
func (s *AuthService) GetUser(ctx context.Context, userID int64) (*model.User, error) {
	return s.userRepo.GetUserByID(ctx, userID)
}

// UpdateUserRole обновляет роль пользователя
func (s *AuthService) UpdateUserRole(ctx context.Context, userID int64, role string) (*model.User, error) {
	return s.userRepo.UpdateUserRole(ctx, userID, role)
}

// BanUser банит/разбанивает пользователя
func (s *AuthService) BanUser(ctx context.Context, userID int64, isBanned bool) (*model.User, error) {
	return s.userRepo.BanUser(ctx, userID, isBanned)
}
