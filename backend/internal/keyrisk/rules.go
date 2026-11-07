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
	UsersPerIP      int      `json:"users_per_ip"`
	IPsPerKey       int      `json:"ips_per_key"`
	TrustedNetworks []string `json:"trusted_networks"`
}

func DefaultConfig() Config {
	return Config{Enabled: true, MaxReminders: 3, AgeDays: 90, WindowMinutes: 30, UsersPerIP: 3, IPsPerKey: 5, TrustedNetworks: []string{}}
}
func (c Config) Validate() error {
	if c.MaxReminders < 1 || c.MaxReminders > 10 || c.AgeDays < 1 || c.AgeDays > 3650 || c.WindowMinutes < 1 || c.WindowMinutes > 60 || c.UsersPerIP < 2 || c.UsersPerIP > 100 || c.IPsPerKey < 2 || c.IPsPerKey > 100 || len(c.TrustedNetworks) > 256 {
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
	RapidSwitch   bool      `json:"rapid_switch"`
	IPs           []string  `json:"ips"`
	IP            string    `json:"ip,omitempty"`
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
	byIP := map[string][]Request{}
	byKey := map[int64][]Request{}
	for _, r := range rs {
		ip, e := netip.ParseAddr(r.IP)
		if e != nil || r.UserID <= 0 || r.KeyID <= 0 || r.At.Before(now.Add(-time.Duration(c.WindowMinutes)*time.Minute)) || r.At.After(now) {
			continue
		}
		ip = ip.Unmap()
		if trusted(ip, c) {
			continue
		}
		r.IP = ip.String()
		byIP[r.IP] = append(byIP[r.IP], r)
		byKey[r.KeyID] = append(byKey[r.KeyID], r)
	}
	out := []Finding{}
	risky := map[string]bool{}
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
	for ip, rows := range byIP {
		ev := makeEvidence(rows)
		if len(ev.UserIDs) >= c.UsersPerIP && len(ev.KeyIDs) >= c.UsersPerIP {
			ev.IP = ip
			// A sliding minute distinguishes rapid key testing from a shared exit over a longer window.
			ordered := append([]Request(nil), rows...)
			sort.Slice(ordered, func(i, j int) bool { return ordered[i].At.Before(ordered[j].At) })
			users, keys := map[int64]int{}, map[int64]int{}
			left := 0
			for _, r := range ordered {
				users[r.UserID]++
				keys[r.KeyID]++
				for ordered[left].At.Before(r.At.Add(-time.Minute)) {
					old := ordered[left]
					users[old.UserID]--
					if users[old.UserID] == 0 {
						delete(users, old.UserID)
					}
					keys[old.KeyID]--
					if keys[old.KeyID] == 0 {
						delete(keys, old.KeyID)
					}
					left++
				}
				if len(users) >= c.UsersPerIP && len(keys) >= c.UsersPerIP {
					ev.RapidSwitch = true
					break
				}
			}
			out = append(out, Finding{"cross_user_ip", ip, ev})
			risky[ip] = true
		}
	}
	for key, rows := range byKey {
		ips := map[string]bool{}
		linked := false
		for _, r := range rows {
			ips[r.IP] = true
			linked = linked || risky[r.IP]
		}
		if len(ips) >= c.IPsPerKey {
			rule := "key_ip_spread"
			if linked {
				rule = "key_ip_spread_linked"
			}
			out = append(out, Finding{rule, strconv.FormatInt(key, 10), makeEvidence(rows)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rule+out[i].Subject < out[j].Rule+out[j].Subject })
	return out
}
