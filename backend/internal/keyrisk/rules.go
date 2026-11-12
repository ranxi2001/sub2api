// Package keyrisk performs bounded, off-path credential risk assessment.
package keyrisk

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"time"
)

type Config struct {
	Enabled         bool     `json:"enabled"`
	AutoEmail       bool     `json:"auto_email"`
	AgeEmail        bool     `json:"age_email"`
	MaxReminders    int      `json:"max_reminders"`
	AgeDays         int      `json:"age_days"`
	WindowMinutes   int      `json:"window_minutes"`
	IPsPerKey       int      `json:"ips_per_key"`
	TrustedNetworks []string `json:"trusted_networks"`
}

func DefaultConfig() Config {
	return Config{Enabled: true, MaxReminders: 3, AgeDays: 90, WindowMinutes: 30, IPsPerKey: 2, TrustedNetworks: []string{}}
}
func (c Config) Validate() error {
	if c.MaxReminders < 1 || c.MaxReminders > 10 || c.AgeDays < 1 || c.AgeDays > 3650 || c.WindowMinutes < 1 || c.WindowMinutes > 60 || c.IPsPerKey < 2 || c.IPsPerKey > 100 || len(c.TrustedNetworks) > 256 {
		return fmt.Errorf("invalid key risk thresholds")
	}
	for _, s := range c.TrustedNetworks {
		if _, err := netip.ParsePrefix(s); err != nil {
			if _, err = netip.ParseAddr(s); err != nil {
				return fmt.Errorf("invalid trusted network")
			}
		}
	}
	return nil
}

type Request struct {
	IP            string
	UserID, KeyID int64
	At            time.Time
}
type Evidence struct {
	IPs           []string  `json:"ips"`
	UserIDs       []int64   `json:"user_ids"`
	KeyIDs        []int64   `json:"key_ids"`
	Requests      int       `json:"requests"`
	WindowMinutes int       `json:"window_minutes"`
	First         time.Time `json:"first"`
	Last          time.Time `json:"last"`
}
type Finding struct {
	Rule     string   `json:"rule"`
	Subject  string   `json:"subject"`
	Evidence Evidence `json:"evidence"`
}

func ids(m map[int64]bool) []int64 {
	a := make([]int64, 0, len(m))
	for v := range m {
		a = append(a, v)
	}
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
	return a
}
func trusted(ip netip.Addr, c Config) bool {
	for _, s := range c.TrustedNetworks {
		if p, e := netip.ParsePrefix(s); e == nil && p.Contains(ip) {
			return true
		}
		if a, e := netip.ParseAddr(s); e == nil && a.Unmap() == ip {
			return true
		}
	}
	return false
}

// Evaluate requires authenticated attribution. UA and polling cadence alone never imply leakage.
func Evaluate(rs []Request, c Config, now time.Time) []Finding {
	byKey := map[int64][]Request{}
	for _, r := range rs {
		ip, e := netip.ParseAddr(r.IP)
		if e != nil || ip.Zone() != "" || r.UserID <= 0 || r.KeyID <= 0 || r.At.Before(now.Add(-time.Duration(c.WindowMinutes)*time.Minute)) || r.At.After(now) {
			continue
		}
		ip = ip.Unmap()
		r.IP = ip.String()
		byKey[r.KeyID] = append(byKey[r.KeyID], r)
	}
	out := []Finding{}
	makeEvidence := func(rows []Request) Evidence {
		u, k := map[int64]bool{}, map[int64]bool{}
		sources := map[string]bool{}
		first, last := rows[0].At, rows[0].At
		for _, r := range rows {
			sources[r.IP] = true
			u[r.UserID] = true
			k[r.KeyID] = true
			if r.At.Before(first) {
				first = r.At
			}
			if r.At.After(last) {
				last = r.At
			}
		}
		ips := make([]string, 0, len(sources))
		for ip := range sources {
			ips = append(ips, ip)
		}
		sort.Strings(ips)
		return Evidence{IPs: ips, UserIDs: ids(u), KeyIDs: ids(k), Requests: len(rows), WindowMinutes: c.WindowMinutes, First: first, Last: last}
	}
	for key, rows := range byKey {
		ev := makeEvidence(rows)
		allTrusted := true
		for _, raw := range ev.IPs {
			addr, _ := netip.ParseAddr(raw)
			allTrusted = allTrusted && trusted(addr, c)
		}
		// Retain trusted sources in the count: one approved exit plus an unknown
		// source is still cross-IP use. Only wholly trusted source sets are exempt.
		if len(ev.IPs) >= c.IPsPerKey && !allTrusted {
			out = append(out, Finding{"key_ip_spread", strconv.FormatInt(key, 10), ev})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rule+out[i].Subject < out[j].Rule+out[j].Subject })
	return out
}
