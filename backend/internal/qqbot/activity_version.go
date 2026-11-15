package qqbot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

type configVersionContextKey struct{}

func WithConfigVersion(ctx context.Context, version string) context.Context {
	version = strings.TrimSpace(version)
	version = strings.TrimPrefix(version, "W/")
	version = strings.TrimSpace(version)
	if len(version) >= 2 && version[0] == '"' && version[len(version)-1] == '"' {
		version = version[1 : len(version)-1]
	}
	// Caddy appends the selected content encoding to strong ETags. Older
	// frontends send that transformed ETag as If-Match, so normalize only the
	// known encoding suffixes to keep those already-open pages working.
	version = strings.TrimSuffix(version, "-gzip")
	version = strings.TrimSuffix(version, "-zstd")
	return context.WithValue(ctx, configVersionContextKey{}, version)
}
func ConfigVersion(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func checkConfigVersion(ctx context.Context, value any) error {
	if expected, ok := ctx.Value(configVersionContextKey{}).(string); ok && (expected == "" || expected != ConfigVersion(value)) {
		return ErrConfigConflict
	}
	return nil
}
func inviteConfigOf(c Config) InviteConfig {
	r := c.inviteRechargeRequirement()
	r.RechargeWindowDays = r.windowDays()
	return InviteConfig{Enabled: c.InviteEnabled, InviteReward: c.InviteReward, InviteDailyLimit: c.InviteDailyLimit, CouponTemplateID: c.InviteCouponTemplateID, CouponQuantity: c.InviteCouponQuantity, RechargeRequirement: r}
}
func transferConfigOf(c Config) TransferConfig {
	return TransferConfig{TransferEnabled: c.TransferEnabled, TransferFeePercent: c.TransferFeePercent, TransferMinAmount: c.TransferMinAmount, TransferMaxAmount: c.TransferMaxAmount}
}

type checkinSectionContextKey struct{}

func WithCheckinSection(ctx context.Context, section string) context.Context {
	return context.WithValue(ctx, checkinSectionContextKey{}, section)
}
func CheckinSectionVersion(c CheckinConfig, section string) string {
	switch section {
	case "checkin":
		c.Growth = GrowthConfig{}
		return ConfigVersion(c)
	case "tasks":
		return ConfigVersion(c.Growth.Tasks)
	case "periodic_tasks":
		return ConfigVersion(c.Growth.PeriodicTasks)
	}
	return ConfigVersion(c)
}
func checkCheckinVersion(ctx context.Context, c CheckinConfig, section string) error {
	if v, ok := ctx.Value(configVersionContextKey{}).(string); ok && (v == "" || v != CheckinSectionVersion(c, section)) {
		return ErrConfigConflict
	}
	return nil
}
