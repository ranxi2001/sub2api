package service

import (
	"encoding/json"
	"net/netip"
	"slices"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type LotteryInviteConfig struct {
	RewardTo           string   `json:"reward_to"`
	Tickets            int64    `json:"tickets"`
	DailyLimit         int      `json:"daily_limit"`
	IPDailyLimit       int      `json:"ip_daily_limit"`
	BlockedIPs         []string `json:"blocked_ips,omitempty"`
	BlockedCountries   []string `json:"blocked_countries,omitempty"`
	DenyUnknownCountry bool     `json:"deny_unknown_country"`
}

func lotteryPublicPool(v *LotteryPoolView) {
	if v.InviteConfig != nil {
		v.InviteConfig.BlockedIPs = nil
		v.InviteConfig.BlockedCountries = nil
	}
}

func lotteryInviteMode(mode string) bool {
	return mode == "invite_visit" || mode == "invite_signup" || mode == "invite_recharge"
}

func lotteryPoolInviteConfig(p *dbent.LotteryPool) LotteryInviteConfig {
	config := LotteryInviteConfig{RewardTo: "inviter", Tickets: 1, DailyLimit: 10, IPDailyLimit: 1, DenyUnknownCountry: true}
	if p.InviteConfig != nil {
		b, _ := json.Marshal(p.InviteConfig)
		_ = json.Unmarshal(b, &config)
	}
	return config
}

func lotteryInviteConfigMap(config *LotteryInviteConfig) map[string]any {
	if config == nil {
		return nil
	}
	data, _ := json.Marshal(config)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

func normalizeLotteryInviteConfig(input *LotteryInviteConfig) (*LotteryInviteConfig, error) {
	if input == nil {
		defaults := lotteryPoolInviteConfig(&dbent.LotteryPool{})
		input = &defaults
	}
	c := *input
	if (c.RewardTo != "inviter" && c.RewardTo != "both") || c.Tickets < 1 || c.Tickets > 100 || c.DailyLimit < 1 || c.DailyLimit > 1000 || c.IPDailyLimit < 1 || c.IPDailyLimit > 100 {
		return nil, infraerrors.BadRequest("LOTTERY_INVALID_INVITE_RULE", "invalid invitation recipient, tickets, or daily limits")
	}
	if len(c.BlockedIPs) > 500 || len(c.BlockedCountries) > 250 {
		return nil, infraerrors.BadRequest("LOTTERY_INVALID_INVITE_RULE", "too many blocked IPs or countries")
	}
	c.BlockedIPs = append([]string{}, input.BlockedIPs...)
	for i, raw := range c.BlockedIPs {
		raw = strings.TrimSpace(raw)
		if ip, err := netip.ParseAddr(raw); err == nil && ip.Zone() == "" {
			c.BlockedIPs[i] = ip.Unmap().String()
		} else if prefix, err := netip.ParsePrefix(raw); err == nil {
			if prefix.Addr().Is4In6() {
				if prefix.Bits() < 96 {
					return nil, infraerrors.BadRequest("LOTTERY_INVALID_IP", "use a canonical IPv4 or IPv6 network")
				}
				prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
			}
			c.BlockedIPs[i] = prefix.Masked().String()
		} else {
			return nil, infraerrors.BadRequest("LOTTERY_INVALID_IP", "invalid IP address or CIDR network")
		}
	}
	c.BlockedCountries = append([]string{}, input.BlockedCountries...)
	for i, raw := range c.BlockedCountries {
		code := strings.ToUpper(strings.TrimSpace(raw))
		if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
			return nil, infraerrors.BadRequest("LOTTERY_INVALID_COUNTRY", "use two-letter country codes")
		}
		c.BlockedCountries[i] = code
	}
	slices.Sort(c.BlockedIPs)
	c.BlockedIPs = slices.Compact(c.BlockedIPs)
	slices.Sort(c.BlockedCountries)
	c.BlockedCountries = slices.Compact(c.BlockedCountries)
	return &c, nil
}

func lotteryVisitAllowed(config LotteryInviteConfig, address, country string) bool {
	ip, err := netip.ParseAddr(address)
	if err != nil || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	for _, raw := range config.BlockedIPs {
		if blocked, err := netip.ParseAddr(raw); err == nil && blocked.Unmap() == ip {
			return false
		}
		if prefix, err := netip.ParsePrefix(raw); err == nil && prefix.Contains(ip) {
			return false
		}
	}
	if country == "" && config.DenyUnknownCountry {
		return false
	}
	return !slices.Contains(config.BlockedCountries, country)
}
