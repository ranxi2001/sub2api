package admin

import (
	"context"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/keyrisk"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
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

type keyRiskKeyUpdater struct {
	calls     int
	id, owner int64
	whitelist []string
}

func (k *keyRiskKeyUpdater) Update(_ context.Context, id, owner int64, req service.UpdateAPIKeyRequest) (*service.APIKey, error) {
	k.calls++
	k.id = id
	k.owner = owner
	k.whitelist = *req.IPWhitelist
	requireOnlyWhitelist := req
	requireOnlyWhitelist.IPWhitelist = nil
	if !reflect.DeepEqual(requireOnlyWhitelist, service.UpdateAPIKeyRequest{}) {
		return nil, fmt.Errorf("unexpected fields")
	}
	return &service.APIKey{}, nil
}
func riskActionRequest(t *testing.T, h *SettingHandler, role, body string) *httptest.ResponseRecorder {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 17})
		c.Set(string(middleware.ContextKeyUserRole), role)
	})
	r.POST("/test/:id", h.ActOnKeyRisk)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/test/9", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}
func TestKeyRiskRestrictionOnlyUpdatesCurrentKeysWhitelist(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
	})
	updater := &keyRiskKeyUpdater{}
	h := &SettingHandler{keyRisk: &keyrisk.Service{DB: db}, keyRiskKeys: updater}
	payload := `{"action":"restrict","generation":2,"confirm":true,"allowed_networks":["192.0.2.1","::ffff:192.0.2.1","198.51.100.18/24"]}`
	require.Equal(t, 403, riskActionRequest(t, h, "user", payload).Code)
	require.Zero(t, updater.calls)
	mock.ExpectQuery("SELECT k.id,k.user_id FROM key_risk_events").WithArgs(int64(9), int64(2)).WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}).AddRow(11, 1))
	w := riskActionRequest(t, h, "admin", payload)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, 1, updater.calls)
	require.EqualValues(t, 11, updater.id)
	require.EqualValues(t, 1, updater.owner)
	require.Equal(t, []string{"192.0.2.1/32", "198.51.100.0/24"}, updater.whitelist)
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestKeyRiskRejectsUnsafeAndStaleRestrictionActions(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
	})
	updater := &keyRiskKeyUpdater{}
	h := &SettingHandler{keyRisk: &keyrisk.Service{DB: db}, keyRiskKeys: updater}
	for _, payload := range []string{
		`{"action":"block"}`,
		`{"action":"restrict","generation":2,"allowed_networks":["192.0.2.1"]}`,
		`{"action":"restrict","confirm":true,"allowed_networks":["192.0.2.1"]}`,
		`{"action":"restrict","generation":2,"confirm":true,"allowed_networks":[]}`,
		`{"action":"restrict","generation":2,"confirm":true,"allowed_networks":["bad"]}`,
	} {
		require.Equal(t, 400, riskActionRequest(t, h, "admin", payload).Code, payload)
	}
	mock.ExpectQuery("SELECT k.id,k.user_id FROM key_risk_events").WithArgs(int64(9), int64(1)).WillReturnError(sql.ErrNoRows)
	require.Equal(t, 409, riskActionRequest(t, h, "admin", `{"action":"restrict","generation":1,"confirm":true,"allowed_networks":["192.0.2.1"]}`).Code)
	require.Zero(t, updater.calls)
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestKeyRiskAllowedNetworks(t *testing.T) {
	for _, values := range [][]string{nil, {}, make([]string, 257), {"bad"}, {"fe80::1%eth0"}} {
		_, err := keyRiskAllowedNetworks(values)
		require.Error(t, err)
	}
	result, err := keyRiskAllowedNetworks([]string{"2001:db8::1", "2001:0db8::1"})
	require.NoError(t, err)
	require.Equal(t, []string{"2001:db8::1/128"}, result)
}

func TestUserKeyRiskRequiresAuthentication(t *testing.T) {
	h := &SettingHandler{}
	require.Equal(t, 401, auditAdminRequest(t, h.GetMyKeyRisks, "GET", "/test", "", "").Code)
	require.Equal(t, 401, auditAdminRequest(t, h.AcknowledgeMyKeyRisk, "POST", "/test", "", `{"generation":1,"confirm":true}`).Code)
	// Ordinary customers reach the user-facing service, not the administrator gate.
	require.Equal(t, 503, auditAdminRequest(t, h.GetMyKeyRisks, "GET", "/test", "user", "").Code)
}
