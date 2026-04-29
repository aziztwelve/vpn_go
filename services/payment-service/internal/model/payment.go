package model

import "time"

// Status значения согласно CHECK-constraint миграций 001/002/003.
//
// Финальные статусы:           pending → paid | failed | cancelled | refunded
// Промежуточные (миграция 003): paid_db_only → paid_subscription_done → paid
//
// Промежуточные используются service.handleSuccessfulPayment'ом как
// чек-поинты: на каждом ретрае webhook'а мы знаем где остановились и
// продолжаем с нужного шага. См. docs/services/payment-integration.md.
const (
	StatusPending          = "pending"
	StatusPaidDBOnly       = "paid_db_only"           // MarkPaid сделан, подписка ещё не создана
	StatusPaidSubscription = "paid_subscription_done" // подписка создана, VPN-юзер не зарегистрирован
	StatusPaid             = "paid"                   // финальный — всё прошло
	StatusFailed           = "failed"
	StatusRefunded         = "refunded"
	StatusCancelled        = "cancelled"
)

// IsPaidIntermediate возвращает true если платёж в одном из промежуточных
// "оплачено-но-не-всё-сделано" статусов. Используется sentinel cron'ом для
// поиска зависших платежей и в service для определения "с какого шага
// продолжать". Финальный paid сюда НЕ входит.
func IsPaidIntermediate(status string) bool {
	return status == StatusPaidDBOnly || status == StatusPaidSubscription
}

// Провайдеры платежей.
const (
	ProviderTelegramStars = "telegram_stars"
	ProviderYooMoney      = "yoomoney"
	ProviderYooKassa      = "yookassa"
)

type Payment struct {
	ID          int64
	UserID      int64
	PlanID      int32
	MaxDevices  int32
	AmountStars int32   // для Telegram Stars
	AmountRUB   float64 // для YooMoney/ЮKassa
	Currency    string  // XTR, RUB, USD
	Status      string
	// ExternalID — ID платежа в системе провайдера.
	// Для Telegram: telegram_payment_charge_id
	// Для YooMoney/YooKassa: payment_id
	ExternalID string
	Provider   string
	Metadata   map[string]string // JSONB, дополнительные данные
	CreatedAt  time.Time
	PaidAt     *time.Time
}
