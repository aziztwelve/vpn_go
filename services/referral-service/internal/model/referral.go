package model

import "time"

// ReferralLink — уникальная ссылка одного юзера. Один user_id = один token.
type ReferralLink struct {
	ID         int64
	UserID     int64
	Token      string
	ClickCount int32
	CreatedAt  time.Time
}

// ReferralRelationship — связь "пригласитель → приглашённый".
// Один invited_id может иметь только одного inviter_id (UNIQUE).
type ReferralRelationship struct {
	InviterID int64
	InvitedID int64
	Status    string // RelationshipStatusRegistered, RelationshipStatusPurchased
	CreatedAt time.Time
}

const (
	RelationshipStatusRegistered = "registered"
	RelationshipStatusPurchased  = "purchased"
)

// ReferralBonus — единичное начисление бонуса.
//   - BonusType=days       → DaysAmount задан, BalanceAmount=nil
//   - BonusType=balance    → BalanceAmount задан, DaysAmount=nil
//
// IsApplied=true означает что бонус уже отдан получателю (продлили подписку
// или зачислили на баланс) — нужно для аудита.
type ReferralBonus struct {
	ID            int64
	UserID        int64    // получатель (inviter)
	InvitedUserID int64    // кто принёс бонус
	BonusType     string   // BonusTypeDays | BonusTypeBalance
	DaysAmount    *int32
	BalanceAmount *float64
	PaymentID     *int64   // ссылка на payment если bonus вызван оплатой (для идемпотентности)
	IsApplied     bool
	CreatedAt     time.Time
}

const (
	BonusTypeDays    = "days"
	BonusTypeBalance = "balance"
)

// WithdrawalRequest — заявка партнёра на вывод средств.
// Создаётся юзером, обрабатывается админом (см. admin-service).
type WithdrawalRequest struct {
	ID             int64
	UserID         int64
	Amount         float64
	PaymentMethod  string
	PaymentDetails map[string]string
	Status         string // WithdrawalStatus*
	AdminComment   string
	CreatedAt      time.Time
	ProcessedAt    *time.Time
}

const (
	WithdrawalStatusPending  = "pending"
	WithdrawalStatusApproved = "approved"
	WithdrawalStatusRejected = "rejected"
	WithdrawalStatusPaid     = "paid"
)

// Skip-причины для RegisterReferral — публичные константы чтобы можно было
// сравнивать со строкой из proto.
const (
	SkipReasonSelfInvite     = "self_invite"
	SkipReasonTokenNotFound  = "token_not_found"
	SkipReasonAlreadyInvited = "already_invited"
	SkipReasonUserTooOld     = "user_too_old"
	SkipReasonInvitedNotFound = "invited_not_found"
)
