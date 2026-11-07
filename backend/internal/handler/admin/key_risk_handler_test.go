package admin

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestKeyRiskAdminAuthority(t *testing.T) {
	h := &SettingHandler{}
	for _, role := range []string{"", "user", "observer"} {
		require.Equal(t, 403, auditAdminRequest(t, h.GetKeyRisk, "GET", "/test", role, "").Code)
		require.Equal(t, 403, auditAdminRequest(t, h.SaveKeyRisk, "PUT", "/test", role, `{}`).Code)
		require.Equal(t, 403, auditAdminRequest(t, h.ActOnKeyRisk, "POST", "/test", role, `{}`).Code)
	}
	require.Equal(t, 503, auditAdminRequest(t, h.GetKeyRisk, "GET", "/test", "admin", "").Code)
}
func TestKeyRiskManualBlockAndSelfProtection(t *testing.T) {
	h, _ := auditFixture(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/test", nil)
	c.Request.RemoteAddr = "192.0.2.10:1234"
	require.NoError(t, h.blockKeyRiskIP(context.Background(), c, "192.0.2.20"))
	cfg, e := h.settingService.IPRisk.Config(context.Background())
	require.NoError(t, e)
	found := false
	for _, r := range cfg.Blacklist {
		if r.Network == "192.0.2.20/32" {
			found = true
			require.Greater(t, r.ExpiresAt, int64(0))
		}
	}
	require.True(t, found)
	require.Error(t, h.blockKeyRiskIP(context.Background(), c, "192.0.2.10"))
}

func TestKeyRiskBlockNormalizesAndDeduplicates(t *testing.T) {
	h, _ := auditFixture(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/test", nil)
	c.Request.RemoteAddr = "192.0.2.10:1234"
	for _, ip := range []string{"192.0.2.20", "192.0.2.20", "::ffff:192.0.2.20"} {
		require.NoError(t, h.blockKeyRiskIP(context.Background(), c, ip))
	}
	cfg, e := h.settingService.IPRisk.Config(context.Background())
	require.NoError(t, e)
	n := 0
	for _, r := range cfg.Blacklist {
		if r.Network == "192.0.2.20/32" {
			n++
			require.True(t, r.Enabled)
		}
	}
	require.Equal(t, 1, n)
	// A permanent ban must survive a request for a shorter temporary ban.
	for i := range cfg.Blacklist {
		if cfg.Blacklist[i].Network == "192.0.2.20/32" {
			cfg.Blacklist[i].ExpiresAt = 0
		}
	}
	require.NoError(t, h.settingService.IPRisk.Save(context.Background(), cfg))
	require.NoError(t, h.blockKeyRiskIP(context.Background(), c, "192.0.2.20"))
	cfg, e = h.settingService.IPRisk.Config(context.Background())
	require.NoError(t, e)
	for _, r := range cfg.Blacklist {
		if r.Network == "192.0.2.20/32" {
			require.Zero(t, r.ExpiresAt)
		}
	}
}

func TestKeyRiskMappedAllowlistAndIPv6Duplicates(t *testing.T) {
	h, _ := auditFixture(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/test", nil)
	c.Request.RemoteAddr = "192.0.2.10:1234"
	cfg, e := h.settingService.IPRisk.Config(context.Background())
	require.NoError(t, e)
	cfg.Whitelist = []string{"192.0.2.20/32"}
	require.NoError(t, h.settingService.IPRisk.Save(context.Background(), cfg))
	require.Error(t, h.blockKeyRiskIP(context.Background(), c, "::ffff:192.0.2.20"))
	for _, ip := range []string{"2001:db8::1", "2001:db8:0:0:0:0:0:1"} {
		require.NoError(t, h.blockKeyRiskIP(context.Background(), c, ip))
	}
	cfg, e = h.settingService.IPRisk.Config(context.Background())
	require.NoError(t, e)
	n := 0
	for _, r := range cfg.Blacklist {
		if r.Network == "2001:db8::1/128" {
			n++
		}
	}
	require.Equal(t, 1, n)
}

func TestUserKeyRiskRequiresAuthentication(t *testing.T) {
	h := &SettingHandler{}
	require.Equal(t, 401, auditAdminRequest(t, h.GetMyKeyRisks, "GET", "/test", "", "").Code)
	require.Equal(t, 401, auditAdminRequest(t, h.AcknowledgeMyKeyRisk, "POST", "/test", "", `{"generation":1,"confirm":true}`).Code)
	// Ordinary customers reach the user-facing service, not the administrator gate.
	require.Equal(t, 503, auditAdminRequest(t, h.GetMyKeyRisks, "GET", "/test", "user", "").Code)
}
