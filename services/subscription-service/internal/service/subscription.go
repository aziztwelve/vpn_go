package service

import (
	"context"

	"github.com/vpn/subscription-service/internal/model"
	"github.com/vpn/subscription-service/internal/repository"
	"go.uber.org/zap"
)

type SubscriptionService struct {
	repo   *repository.SubscriptionRepository
	logger *zap.Logger
}

func NewSubscriptionService(repo *repository.SubscriptionRepository, logger *zap.Logger) *SubscriptionService {
	return &SubscriptionService{
		repo:   repo,
		logger: logger,
	}
}

func (s *SubscriptionService) ListPlans(ctx context.Context, activeOnly bool) ([]*model.SubscriptionPlan, error) {
	return s.repo.ListPlans(ctx, activeOnly)
}

func (s *SubscriptionService) GetDevicePricing(ctx context.Context, planID int32) ([]*model.DeviceAddonPricing, error) {
	return s.repo.GetDevicePricing(ctx, planID)
}

// GetRateToRub — прозрачный pass-through к repo (см. currency_rates).
func (s *SubscriptionService) GetRateToRub(ctx context.Context, currency string) (float64, error) {
	return s.repo.GetRateToRub(ctx, currency)
}

func (s *SubscriptionService) CreateSubscription(ctx context.Context, userID int64, planID int32, maxDevices int32, totalPrice float64) (*model.Subscription, error) {
	sub, err := s.repo.CreateSubscription(ctx, userID, planID, maxDevices, totalPrice)
	if err != nil {
		return nil, err
	}

	s.logger.Info("Subscription created",
		zap.Int64("user_id", userID),
		zap.Int64("subscription_id", sub.ID),
		zap.Int32("plan_id", planID),
	)

	return sub, nil
}

func (s *SubscriptionService) GetActiveSubscription(ctx context.Context, userID int64) (*model.Subscription, bool, error) {
	sub, err := s.repo.GetActiveSubscription(ctx, userID)
	if err != nil {
		return nil, false, nil // No active subscription
	}

	return sub, true, nil
}

func (s *SubscriptionService) ExtendSubscription(ctx context.Context, subscriptionID int64, days int32) (*model.Subscription, error) {
	sub, err := s.repo.ExtendSubscription(ctx, subscriptionID, days)
	if err != nil {
		return nil, err
	}

	s.logger.Info("Subscription extended",
		zap.Int64("subscription_id", subscriptionID),
		zap.Int32("days", days),
	)

	return sub, nil
}

func (s *SubscriptionService) CancelSubscription(ctx context.Context, subscriptionID int64) (*model.Subscription, error) {
	sub, err := s.repo.CancelSubscription(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}

	s.logger.Info("Subscription cancelled", zap.Int64("subscription_id", subscriptionID))

	return sub, nil
}

func (s *SubscriptionService) CheckSubscriptionActive(ctx context.Context, userID int64) (bool, int64, string, int32, error) {
	sub, hasActive, err := s.GetActiveSubscription(ctx, userID)
	if err != nil {
		return false, 0, "", 0, err
	}

	if !hasActive {
		return false, 0, "", 0, nil
	}

	return true, sub.ID, sub.ExpiresAt.Format("2006-01-02T15:04:05Z"), sub.MaxDevices, nil
}

func (s *SubscriptionService) GetSubscriptionHistory(ctx context.Context, userID int64) ([]*model.Subscription, error) {
	return s.repo.GetSubscriptionHistory(ctx, userID)
}

// StartTrial активирует пробный период юзеру, если ещё не выдан.
// Возвращает (sub, alreadyUsed, err). alreadyUsed=true — триал уже был выдан
// этому телеграм-аккаунту (по users.trial_used_at), новой подписки не создаём.
func (s *SubscriptionService) StartTrial(ctx context.Context, userID int64) (*model.Subscription, bool, error) {
	trialPlan, err := s.repo.GetTrialPlan(ctx)
	if err != nil {
		return nil, false, err
	}

	sub, alreadyUsed, err := s.repo.StartTrialTx(ctx, userID, trialPlan)
	if err != nil {
		return nil, false, err
	}

	if alreadyUsed {
		s.logger.Info("StartTrial: already used",
			zap.Int64("user_id", userID))
		return nil, true, nil
	}

	s.logger.Info("Trial started",
		zap.Int64("user_id", userID),
		zap.Int64("subscription_id", sub.ID),
		zap.Int32("duration_days", trialPlan.DurationDays),
		zap.Time("expires_at", sub.ExpiresAt),
	)
	return sub, false, nil
}

// ClaimChannelBonus начисляет +3 дня к активной подписке за подписку на канал.
// Идемпотентно — если users.channel_bonus_claimed=true, возвращает alreadyClaimed=true.
func (s *SubscriptionService) ClaimChannelBonus(ctx context.Context, userID int64) (*model.Subscription, bool, bool, error) {
	sub, alreadyClaimed, noActiveSub, err := s.repo.ClaimChannelBonusTx(ctx, userID)
	if err != nil {
		return nil, false, false, err
	}

	if alreadyClaimed {
		s.logger.Info("ClaimChannelBonus: already claimed",
			zap.Int64("user_id", userID))
		return nil, true, false, nil
	}

	if noActiveSub {
		s.logger.Info("ClaimChannelBonus: no active subscription",
			zap.Int64("user_id", userID))
		return nil, false, true, nil
	}

	s.logger.Info("Channel bonus claimed",
		zap.Int64("user_id", userID),
		zap.Int64("subscription_id", sub.ID),
		zap.Time("new_expires_at", sub.ExpiresAt),
	)
	return sub, false, false, nil
}
