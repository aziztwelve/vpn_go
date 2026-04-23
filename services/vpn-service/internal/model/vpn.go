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
	ID             int64
	UserID         int64
	SubscriptionID int64
	UUID           string
	Email          string
	Flow           string
	CreatedAt      time.Time
}

type ActiveConnection struct {
	ID               int64
	VPNUserID        int64
	ServerID         int32
	DeviceIdentifier string
	ConnectedAt      time.Time
	LastSeen         time.Time
}
