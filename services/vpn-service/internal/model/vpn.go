package model

import "time"

type VPNServer struct {
	ID                   int32
	Name                 string
	Location             string
	CountryCode          string
	Host                 string
	Port                 int32
	PublicKey            string
	PrivateKey           string
	ShortID              string
	Dest                 string
	ServerNames          string
	XrayAPIHost          string
	XrayAPIPort          int32
	InboundTag           string
	IsActive             bool
	LoadPercent          int32
	ServerMaxConnections int32
	Description          string
	CreatedAt            time.Time
}

type VPNUser struct {
	ID                int64
	UserID            int64
	SubscriptionID    int64
	UUID              string
	Email             string
	Flow              string
	SubscriptionToken string
	CreatedAt         time.Time
}

// SubscriptionConfig — данные, собранные для публичной подписки
// по subscription_token. Репо делает один JOIN vpn_users+subscriptions,
// плюс отдельный SELECT активных серверов.
type SubscriptionConfig struct {
	VPNUser    *VPNUser
	Servers    []*VPNServer
	ExpiresAt  time.Time
	MaxDevices int32
}

// ActiveConnection — запись в таблице subscription_fetches (бывш.
// active_connections) о скачивании клиентом subscription URL (Happ,
// V2rayTUN, Telegram link-preview) ИЛИ per-server GetVLESSLink вызове.
// Идентичность — пара (vpn_user_id, device_identifier) где
// device_identifier — нормализованный User-Agent HTTP-запроса.
//
// Historical naming: до 2026-05-04 Heartbeat также обновлял last_seen
// при росте трафика — это создавало неоднозначность «fetch vs real
// traffic». После миграции 007 + изменений в Heartbeat таблица содержит
// ТОЛЬКО fetch-события. Реальный трафик теперь живёт в traffic_samples
// (миграция 008). Go-тип и proto-сообщение сохраняют имя ActiveConnection
// для обратной совместимости gRPC API (gateway/frontend).
type ActiveConnection struct {
	ID               int64
	VPNUserID        int64
	ServerID         int32
	DeviceIdentifier string
	ConnectedAt      time.Time
	LastSeen         time.Time
}

// TrafficSample — один сэмпл трафика юзера на одном сервере за
// интервал между тиками TrafficCron (5 мин). Дельты, не кумулятив —
// благодаря reset=true в xray stats после каждого чтения.
type TrafficSample struct {
	ID            int64
	VPNUserID     int64
	ServerID      int32
	UplinkBytes   int64
	DownlinkBytes int64
	CollectedAt   time.Time
}
