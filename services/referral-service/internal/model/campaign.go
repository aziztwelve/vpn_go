package model

import "time"

// Campaign — маркетинговая воронка/UTM-кампания.
//
// Slug используется в start-параметре Telegram (https://t.me/<bot>?start=src_<slug>).
// Длина ≤ 60 чтобы вместе с префиксом "src_" уместиться в Telegram-лимит 64.
type Campaign struct {
	ID                 int64
	Slug               string
	Name               string
	Notes              string
	PartnerUserID      *int64 // NULL = без выплат партнёру
	PayoutPercent      *int32 // NULL = без выплат
	IsActive           bool
	CreatedBy          int64
	CreatedAt          time.Time
	ArchivedAt         *time.Time // NULL = активна
	TrialDurationDays  *int32     // NULL = дефолт (subscription_plans.id=99, 3 дня); 3/7/15/30/60/90 = override (см. task 19)
}

// AllowedTrialDurationDays — единый источник правды по разрешённым пресетам
// для override длительности триала (см. task 19). Совпадает с CHECK'ом в
// миграции 004_campaign_trial_override.up.sql.
var AllowedTrialDurationDays = []int32{3, 7, 15, 30, 60, 90}

// IsValidTrialDurationDays — true если значение допустимо как override,
// nil считается валидным (= использовать дефолт).
func IsValidTrialDurationDays(d *int32) bool {
	if d == nil {
		return true
	}
	for _, v := range AllowedTrialDurationDays {
		if v == *d {
			return true
		}
	}
	return false
}

// CampaignStats — агрегированные метрики воронки за период (или за всё время).
type CampaignStats struct {
	CampaignID         int64
	Starts             int32   // /start в боте (bot_starts.campaign_id)
	OpenedApp          int32   // открыли Mini App = атрибутированы (user_attribution)
	TrialActivated     int32   // активировали подписку (subscriptions с этим юзером)
	PaidUsers          int32   // совершили хотя бы одну успешную оплату
	RevenueRUB         float64 // SUM(payments.amount) для completed платежей
	PartnerPayoutsRUB  float64 // SUM(campaign_payouts.amount)
	From               time.Time // нулевое = без нижней границы
	To                 time.Time // нулевое = без верхней границы
}

// CampaignSlugRegex — единый источник правды по формату slug'а.
// Дублируется в БД (CHECK constraint) для двойной защиты от мусора.
const CampaignSlugRegex = `^[a-z0-9_-]{3,60}$`
