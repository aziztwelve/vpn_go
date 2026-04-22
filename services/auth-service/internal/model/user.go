package model

import "time"

type User struct {
	ID           int64
	TelegramID   int64
	Username     string
	FirstName    string
	LastName     string
	PhotoURL     string
	LanguageCode string
	Role         string
	IsBanned     bool
	Balance      float64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastActiveAt *time.Time
}

const (
	RoleUser    = "user"
	RolePartner = "partner"
	RoleAdmin   = "admin"
)
