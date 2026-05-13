package service

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

type RedeemCode struct {
	ID        int64
	Code      string
	Type      string
	Value     float64
	Status    string
	UsedBy    *int64
	UsedAt    *time.Time
	Notes     string
	CreatedAt time.Time

	GroupID      *int64
	ValidityDays int

	// PlanID 钱包模式额度卡：兑换时按 plan.WalletQuotaUsd 创建 wallet 订阅。
	// Type=RedeemTypeWallet 时必填；其它 type 为 nil。链动小铺 credits SKU 走此路径（B2.7）。
	PlanID *int64

	User  *User
	Group *Group
}

func (r *RedeemCode) IsUsed() bool {
	return r.Status == StatusUsed
}

func (r *RedeemCode) CanUse() bool {
	return r.Status == StatusUnused
}

func GenerateRedeemCode() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
