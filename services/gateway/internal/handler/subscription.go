package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vpn/gateway/internal/client"
	"go.uber.org/zap"
)

type SubscriptionHandler struct {
	subscriptionClient *client.SubscriptionClient
	logger             *zap.Logger
}

func NewSubscriptionHandler(subscriptionClient *client.SubscriptionClient, logger *zap.Logger) *SubscriptionHandler {
	return &SubscriptionHandler{
		subscriptionClient: subscriptionClient,
		logger:             logger,
	}
}

func (h *SubscriptionHandler) ListPlans(w http.ResponseWriter, r *http.Request) {
	activeOnly := r.URL.Query().Get("active_only") != "false"

	resp, err := h.subscriptionClient.ListPlans(r.Context(), activeOnly)
	if err != nil {
		h.logger.Error("Failed to list plans", zap.Error(err))
		http.Error(w, "Failed to list plans", http.StatusInternalServerError)
		return
	}

	plans := make([]map[string]interface{}, 0, len(resp.Plans))
	for _, plan := range resp.Plans {
		plans = append(plans, map[string]interface{}{
			"id":            plan.Id,
			"name":          plan.Name,
			"duration_days": plan.DurationDays,
			"max_devices":   plan.MaxDevices,
			"base_price":    plan.BasePrice,
			"is_active":     plan.IsActive,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(plans)
}

func (h *SubscriptionHandler) GetDevicePricing(w http.ResponseWriter, r *http.Request) {
	planIDStr := chi.URLParam(r, "planId")
	planID, err := strconv.ParseInt(planIDStr, 10, 32)
	if err != nil {
		http.Error(w, "Invalid plan ID", http.StatusBadRequest)
		return
	}

	resp, err := h.subscriptionClient.GetDevicePricing(r.Context(), int32(planID))
	if err != nil {
		h.logger.Error("Failed to get device pricing", zap.Error(err))
		http.Error(w, "Failed to get pricing", http.StatusInternalServerError)
		return
	}

	prices := make([]map[string]interface{}, 0, len(resp.Prices))
	for _, price := range resp.Prices {
		prices = append(prices, map[string]interface{}{
			"max_devices": price.MaxDevices,
			"price":       price.Price,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(prices)
}

func (h *SubscriptionHandler) GetActiveSubscription(w http.ResponseWriter, r *http.Request) {
	// TODO: Get user ID from JWT token
	userID := int64(1) // Mock for now

	resp, err := h.subscriptionClient.GetActiveSubscription(r.Context(), userID)
	if err != nil {
		h.logger.Error("Failed to get active subscription", zap.Error(err))
		http.Error(w, "Failed to get subscription", http.StatusInternalServerError)
		return
	}

	result := map[string]interface{}{
		"has_active": resp.HasActive,
	}

	if resp.HasActive && resp.Subscription != nil {
		result["subscription"] = map[string]interface{}{
			"id":          resp.Subscription.Id,
			"user_id":     resp.Subscription.UserId,
			"plan_id":     resp.Subscription.PlanId,
			"plan_name":   resp.Subscription.PlanName,
			"max_devices": resp.Subscription.MaxDevices,
			"total_price": resp.Subscription.TotalPrice,
			"started_at":  resp.Subscription.StartedAt,
			"expires_at":  resp.Subscription.ExpiresAt,
			"status":      resp.Subscription.Status,
			"created_at":  resp.Subscription.CreatedAt,
		}
	} else {
		result["subscription"] = nil
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

type CreateSubscriptionRequest struct {
	PlanID     int32  `json:"plan_id"`
	MaxDevices int32  `json:"max_devices"`
	TotalPrice string `json:"total_price"`
}

func (h *SubscriptionHandler) CreateSubscription(w http.ResponseWriter, r *http.Request) {
	var req CreateSubscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// TODO: Get user ID from JWT token
	userID := int64(1) // Mock for now

	resp, err := h.subscriptionClient.CreateSubscription(r.Context(), userID, req.PlanID, req.MaxDevices, req.TotalPrice)
	if err != nil {
		h.logger.Error("Failed to create subscription", zap.Error(err))
		http.Error(w, "Failed to create subscription", http.StatusInternalServerError)
		return
	}

	subscription := map[string]interface{}{
		"id":          resp.Subscription.Id,
		"user_id":     resp.Subscription.UserId,
		"plan_id":     resp.Subscription.PlanId,
		"plan_name":   resp.Subscription.PlanName,
		"max_devices": resp.Subscription.MaxDevices,
		"total_price": resp.Subscription.TotalPrice,
		"started_at":  resp.Subscription.StartedAt,
		"expires_at":  resp.Subscription.ExpiresAt,
		"status":      resp.Subscription.Status,
		"created_at":  resp.Subscription.CreatedAt,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(subscription)
}

func (h *SubscriptionHandler) GetSubscriptionHistory(w http.ResponseWriter, r *http.Request) {
	// TODO: Get user ID from JWT token
	userID := int64(1) // Mock for now

	resp, err := h.subscriptionClient.GetSubscriptionHistory(r.Context(), userID)
	if err != nil {
		h.logger.Error("Failed to get subscription history", zap.Error(err))
		http.Error(w, "Failed to get history", http.StatusInternalServerError)
		return
	}

	history := make([]map[string]interface{}, 0, len(resp.Subscriptions))
	for _, sub := range resp.Subscriptions {
		history = append(history, map[string]interface{}{
			"id":          sub.Id,
			"user_id":     sub.UserId,
			"plan_id":     sub.PlanId,
			"plan_name":   sub.PlanName,
			"max_devices": sub.MaxDevices,
			"total_price": sub.TotalPrice,
			"started_at":  sub.StartedAt,
			"expires_at":  sub.ExpiresAt,
			"status":      sub.Status,
			"created_at":  sub.CreatedAt,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(history)
}
