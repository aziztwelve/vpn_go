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

type ActiveConnection struct {
	ID               int64
	VPNUserID        int64
	ServerID         int32
	DeviceIdentifier string
	ConnectedAt      time.Time
	LastSeen         time.Time
}
