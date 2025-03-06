package entity

import (
	"time"
)

type User struct {
	ID        string `gorm:"primaryKey"`
	Username  string `gorm:"uniqueIndex"`
	Email     string
	CreatedAt time.Time
}
