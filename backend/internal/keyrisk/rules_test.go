package keyrisk

import (
	"testing"
	"time"
)

func TestRiskRules(t *testing.T) {
	now := time.Now()
	request := func(ip string, user, key int64) Request { return Request{ip, user, key, now} }
	shared := []Request{request("192.0.2.1", 1, 11), request("192.0.2.1", 2, 22), request("192.0.2.1", 3, 33)}
	spread := []Request{request("192.0.2.1", 1, 11), request("192.0.2.2", 1, 11)}
	tests := []struct {
		name            string
		rows            []Request
		networks        []string
		threshold, want int
	}{
		{"team exit is normal", shared, nil, 2, 0},
		{"single key two sources", spread, nil, 2, 1},
		{"configured threshold", spread, nil, 3, 0},
		{"all sources trusted", spread, []string{"192.0.2.0/24"}, 2, 0},
		{"trusted plus unknown still detected", spread, []string{"192.0.2.1"}, 2, 1},
		{"mapped IPv4 is one source", []Request{request("192.0.2.1", 1, 11), request("::ffff:192.0.2.1", 1, 11)}, nil, 2, 0},
		{"IPv6 equivalent forms are one source", []Request{request("2001:db8::1", 1, 11), request("2001:db8:0:0:0:0:0:1", 1, 11)}, nil, 2, 0},
		{"different keys are independent", []Request{request("192.0.2.1", 1, 11), request("192.0.2.2", 1, 12)}, nil, 2, 0},
		{"invalid IP ignored", []Request{request("bad", 1, 11), spread[0]}, nil, 2, 0},
		{"unauthenticated attribution ignored", []Request{request("192.0.2.2", 0, 11), spread[0]}, nil, 2, 0},
		{"old request ignored", []Request{{"192.0.2.2", 1, 11, now.Add(-31 * time.Minute)}, spread[0]}, nil, 2, 0},
		{"future request ignored", []Request{{"192.0.2.2", 1, 11, now.Add(time.Second)}, spread[0]}, nil, 2, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.IPsPerKey = tt.threshold
			cfg.TrustedNetworks = tt.networks
			findings := Evaluate(tt.rows, cfg, now)
			if len(findings) != tt.want {
				t.Fatalf("findings: %+v", findings)
			}
			if tt.want > 0 {
				f := findings[0]
				if f.Rule != "key_ip_spread" || f.Subject != "11" || len(f.Evidence.UserIDs) != 1 || f.Evidence.UserIDs[0] != 1 || len(f.Evidence.KeyIDs) != 1 || f.Evidence.KeyIDs[0] != 11 {
					t.Fatalf("bad key attribution: %+v", f)
				}
			}
		})
	}
}
func TestValidate(t *testing.T) {
	c := DefaultConfig()
	if c.Validate() != nil || c.IPsPerKey != 2 {
		t.Fatal("default invalid")
	}
	c.IPsPerKey = 1
	if c.Validate() == nil {
		t.Fatal("invalid threshold accepted")
	}
	c = DefaultConfig()
	c.TrustedNetworks = []string{"invalid"}
	if c.Validate() == nil {
		t.Fatal("invalid network accepted")
	}
}
func TestReminderLimitValidation(t *testing.T) {
	for _, n := range []int{0, -1, 11} {
		c := DefaultConfig()
		c.MaxReminders = n
		if c.Validate() == nil {
			t.Fatalf("accepted invalid limit %d", n)
		}
	}
	for _, n := range []int{1, 3, 10} {
		c := DefaultConfig()
		c.MaxReminders = n
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
