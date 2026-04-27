package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/vpn/gateway/internal/client"
	pb "github.com/vpn/shared/pkg/proto/subscription/v1"
	"go.uber.org/zap"
)

type BonusHandler struct {
	subscriptionClient *client.SubscriptionClient
	logger             *zap.Logger
	botToken           string
	channelUsername    string
}

func NewBonusHandler(subscriptionClient *client.SubscriptionClient, logger *zap.Logger, botToken, channelUsername string) *BonusHandler {
	return &BonusHandler{
		subscriptionClient: subscriptionClient,
		logger:             logger,
		botToken:           botToken,
		channelUsername:    channelUsername,
	}
}

// CheckChannelSubscription проверяет подписан ли пользователь на канал
func (h *BonusHandler) CheckChannelSubscription(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID int64 `json:"user_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.Error("Failed to decode request", zap.Error(err))
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.UserID == 0 {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}

	// Проверяем подписку через Telegram Bot API
	isSubscribed, err := h.checkTelegramSubscription(req.UserID)
	if err != nil {
		h.logger.Error("Failed to check telegram subscription", zap.Error(err))
		http.Error(w, "Failed to check subscription", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"subscribed": isSubscribed,
	})
}

// ClaimChannelBonus начисляет бонус за подписку на канал
func (h *BonusHandler) ClaimChannelBonus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID int64 `json:"user_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.Error("Failed to decode request", zap.Error(err))
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.UserID == 0 {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}

	// Проверяем подписку через Telegram Bot API
	isSubscribed, err := h.checkTelegramSubscription(req.UserID)
	if err != nil {
		h.logger.Error("Failed to check telegram subscription", zap.Error(err))
		http.Error(w, "Failed to check subscription", http.StatusInternalServerError)
		return
	}

	if !isSubscribed {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "not_subscribed",
			"message": "Сначала подпишитесь на канал",
		})
		return
	}

	// Начисляем бонус через Subscription Service
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	resp, err := h.subscriptionClient.ClaimChannelBonus(ctx, &pb.ClaimChannelBonusRequest{
		UserId: req.UserID,
	})
	if err != nil {
		h.logger.Error("Failed to claim channel bonus", zap.Error(err))
		http.Error(w, "Failed to claim bonus", http.StatusInternalServerError)
		return
	}

	if resp.AlreadyClaimed {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":         false,
			"already_claimed": true,
			"message":         "Вы уже получили этот бонус",
		})
		return
	}

	if resp.NoActiveSubscription {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":               false,
			"no_active_subscription": true,
			"message":                "У вас нет активной подписки",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":      true,
		"message":      "Бонус начислен! +3 дня к подписке",
		"subscription": resp.Subscription,
	})
}

// checkTelegramSubscription проверяет подписку через Telegram Bot API
func (h *BonusHandler) checkTelegramSubscription(userID int64) (bool, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getChatMember?chat_id=%s&user_id=%d",
		h.botToken, h.channelUsername, userID)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var result struct {
		Ok     bool `json:"ok"`
		Result struct {
			Status string `json:"status"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}

	if !result.Ok {
		return false, nil
	}

	// Статусы: creator, administrator, member = подписан
	// left, kicked = не подписан
	status := result.Result.Status
	return status == "creator" || status == "administrator" || status == "member", nil
}
