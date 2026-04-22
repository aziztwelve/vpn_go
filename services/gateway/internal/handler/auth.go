package handler

import (
	"encoding/json"
	"net/http"

	"github.com/vpn/gateway/internal/client"
	"go.uber.org/zap"
)

type AuthHandler struct {
	authClient *client.AuthClient
	logger     *zap.Logger
}

func NewAuthHandler(authClient *client.AuthClient, logger *zap.Logger) *AuthHandler {
	return &AuthHandler{
		authClient: authClient,
		logger:     logger,
	}
}

type ValidateTelegramRequest struct {
	InitData string `json:"init_data"`
}

type ValidateTelegramResponse struct {
	User     interface{} `json:"user"`
	JWTToken string      `json:"jwt_token"`
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *AuthHandler) GetUser(w http.ResponseWriter, r *http.Request) {
	// TODO: Implement
	w.WriteHeader(http.StatusNotImplemented)
}
