package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/vpn/gateway/internal/client"
	"go.uber.org/zap"
)

type AuthHandler struct {
	authClient *client.AuthClient
	subClient  *client.SubscriptionClient
	vpnClient  *client.VPNClient
	logger     *zap.Logger
}

func NewAuthHandler(authClient *client.AuthClient, subClient *client.SubscriptionClient, vpnClient *client.VPNClient, logger *zap.Logger) *AuthHandler {
	return &AuthHandler{
		authClient: authClient,
		subClient:  subClient,
		vpnClient:  vpnClient,
		logger:     logger,
	}
}

type ValidateTelegramRequest struct {
	InitData string `json:"init_data"`
}

type ValidateTelegramResponse struct {
	User           interface{} `json:"user"`
	JWTToken       string      `json:"jwt_token"`
	TrialActivated bool        `json:"trial_activated"`
	Subscription   interface{} `json:"subscription,omitempty"`
}

func (h *AuthHandler) ValidateTelegramUser(w http.ResponseWriter, r *http.Request) {
	var req ValidateTelegramRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Call auth-service gRPC
	resp, err := h.authClient.ValidateTelegramUser(r.Context(), req.InitData)
	if err != nil {
		h.logger.Error("Failed to validate telegram user", zap.Error(err))
		http.Error(w, "Authentication failed", http.StatusUnauthorized)
		return
	}

	// Convert proto to JSON
	result := ValidateTelegramResponse{
		User: map[string]interface{}{
			"id":             resp.User.Id,
			"telegram_id":    resp.User.TelegramId,
			"username":       resp.User.Username,
			"first_name":     resp.User.FirstName,
			"last_name":      resp.User.LastName,
			"photo_url":      resp.User.PhotoUrl,
			"language_code":  resp.User.LanguageCode,
			"role":           resp.User.Role,
			"is_banned":      resp.User.IsBanned,
			"balance":        resp.User.Balance,
			"created_at":     resp.User.CreatedAt,
			"updated_at":     resp.User.UpdatedAt,
			"last_active_at": resp.User.LastActiveAt,
		},
		JWTToken: resp.JwtToken,
	}

	// Для нового юзера — активируем пробный период и VPN.
	// Ошибки тут НЕ валят auth: клиент получит JWT, сможет потом вручную
	// дёрнуть /subscriptions/trial. Фейлы логируются.
	if resp.IsNewUser {
		trialSub := h.activateTrial(r.Context(), resp.User.Id)
		if trialSub != nil {
			result.TrialActivated = true
			result.Subscription = trialSub
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// activateTrial активирует триал новому юзеру и прописывает его UUID в Xray.
// Возвращает сериализованную подписку для ответа клиенту, либо nil если
// что-то упало (см. логи — это не-блокирующая ошибка).
func (h *AuthHandler) activateTrial(ctx context.Context, userID int64) interface{} {
	if h.subClient == nil || h.vpnClient == nil {
		h.logger.Warn("trial activation skipped: sub/vpn client not configured", zap.Int64("user_id", userID))
		return nil
	}

	trialResp, err := h.subClient.StartTrial(ctx, userID)
	if err != nil {
		h.logger.Error("StartTrial failed", zap.Int64("user_id", userID), zap.Error(err))
		return nil
	}
	if trialResp.WasAlreadyUsed {
		// Edge case: юзер уже был новый с точки зрения auth-service, но в
		// sub-service trial_used_at уже стоит (recreate user? ручное удаление?).
		// Не создаём дубликат.
		h.logger.Warn("trial was already used for 'new' user", zap.Int64("user_id", userID))
		return nil
	}

	// VPN-регистрация: UUID в Xray-inbound всех активных серверов.
	if _, err := h.vpnClient.CreateVPNUser(ctx, userID, trialResp.Subscription.Id); err != nil {
		h.logger.Error("CreateVPNUser failed after trial",
			zap.Int64("user_id", userID),
			zap.Int64("subscription_id", trialResp.Subscription.Id),
			zap.Error(err))
		// Триал в БД есть, VPN не работает — пусть клиент вручную ретраится.
		// Но подписку всё равно возвращаем, фронт покажет "пробный период
		// активирован, VPN готовится — обновите через минуту".
	}

	return map[string]interface{}{
		"id":          trialResp.Subscription.Id,
		"plan_id":     trialResp.Subscription.PlanId,
		"plan_name":   trialResp.Subscription.PlanName,
		"max_devices": trialResp.Subscription.MaxDevices,
		"expires_at":  trialResp.Subscription.ExpiresAt,
		"status":      trialResp.Subscription.Status,
	}
}

func (h *AuthHandler) GetUser(w http.ResponseWriter, r *http.Request) {
	// TODO: Implement
	w.WriteHeader(http.StatusNotImplemented)
}
