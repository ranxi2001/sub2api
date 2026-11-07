package keyrisk

import (
	"testing"
	"time"
)

func TestRiskRules(t *testing.T) {
	now := time.Now()
	c := DefaultConfig()
	rs := []Request{{"192.0.2.1", 1, 11, now}, {"192.0.2.1", 2, 22, now}, {"192.0.2.1", 3, 33, now}}
	if f := Evaluate(rs, c, now); len(f) != 1 || f[0].Rule != "cross_user_ip" {
		t.Fatalf("%+v", f)
	}
	c.TrustedNetworks = []string{"192.0.2.0/24"}
	if len(Evaluate(rs, c, now)) != 0 {
		t.Fatal("trusted exit flagged")
	}
	c.TrustedNetworks = nil
	for i := range rs {
		rs[i].UserID = 1
	}
	if len(Evaluate(rs, c, now)) != 0 {
		t.Fatal("same user flagged")
	}
	for i := range rs {
		rs[i].UserID = int64(i + 1)
	}
	for _, ip := range []string{"192.0.2.2", "192.0.2.3", "192.0.2.4", "192.0.2.5"} {
		rs = append(rs, Request{ip, 1, 11, now})
	}
	f := Evaluate(rs, c, now)
	if len(f) != 2 || f[1].Rule != "key_ip_spread_linked" {
		t.Fatalf("linked: %+v", f)
	}
	for i := range rs {
		rs[i].At = now.Add(-time.Hour)
	}
	if len(Evaluate(rs, c, now)) != 0 {
		t.Fatal("old requests included")
	}
}
func TestValidate(t *testing.T) {
	c := DefaultConfig()
	if c.Validate() != nil {
		t.Fatal("default invalid")
	}
	c.UsersPerIP = 1
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
