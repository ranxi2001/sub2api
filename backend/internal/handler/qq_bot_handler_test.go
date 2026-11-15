package handler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type qqTestEncryptor struct{}

func (qqTestEncryptor) Encrypt(s string) (string, error) { return s, nil }
func (qqTestEncryptor) Decrypt(s string) (string, error) { return s, nil }

func TestQQBotBindingManagementRequiresAdmin(t *testing.T) {
	for _, role := range []string{"", "user"} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		h := NewQQBotHandler(qqbot.New(db, qqTestEncryptor{}, true, nil))
		r := gin.New()
		r.Use(func(c *gin.Context) {
			if role != "" {
				c.Set(string(middleware.ContextKeyUserRole), role)
			}
			c.Next()
		})
		r.GET("/bindings", h.ListBindings)
		r.DELETE("/bindings/:user_id", h.Unbind)
		r.GET("/config", h.GetConfig)
		r.PUT("/config", h.SaveConfig)
		r.GET("/runtime", h.Runtime)
		r.POST("/probe/:kind", h.Probe)
		r.GET("/setup/options", h.SetupOptions)
		r.GET("/setup/checks", h.SetupChecks)
		r.PUT("/setup", h.SaveSetup)
		r.POST("/setup/probe/:kind", h.SetupProbe)
		r.GET("/activity-templates", h.ActivityTemplates)
		r.POST("/activity-templates", h.ApplyActivityTemplate)
		for _, route := range []struct{ method, path string }{
			{"GET", "/bindings"}, {"DELETE", "/bindings/7"},
			{"GET", "/config"}, {"PUT", "/config"},
			{"GET", "/runtime"}, {"POST", "/probe/qq"},
			{"GET", "/setup/options"}, {"GET", "/setup/checks"}, {"PUT", "/setup"}, {"POST", "/setup/probe/qq"}, {"GET", "/activity-templates"}, {"POST", "/activity-templates"},
		} {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(route.method, route.path, nil))
			require.Equal(t, 403, w.Code)
		}
		require.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	}
}

func TestQQBotAdminUnbindStaleIdentityReturnsConflict(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("app", 273).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("app", 272).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("DELETE FROM qq_bot_bindings").WillReturnRows(sqlmock.NewRows([]string{"member_id"}))
	mock.ExpectRollback()
	h := NewQQBotHandler(qqbot.New(db, qqTestEncryptor{}, true, nil))
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), "admin"); c.Next() })
	r.DELETE("/bindings/:user_id", h.Unbind)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/bindings/7", strings.NewReader(`{"app_id":"app","member_id":"member","created_at":"2026-10-15T00:00:00Z"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, 409, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQQBotDeleteDiscoveredGroupIsAdminOnlyAndAppScoped(t *testing.T) {
	for _, role := range []string{"", "user", "admin"} {
		t.Run(role, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			if role == "admin" {
				cfg := qqbot.DefaultConfig()
				cfg.AppID = "current-app"
				raw, err := json.Marshal(cfg)
				require.NoError(t, err)
				mock.ExpectQuery("SELECT value FROM settings").WithArgs("qq_bot_config").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(string(raw)))
				mock.ExpectExec("DELETE FROM qq_bot_groups WHERE app_id=\\$1 AND group_id=\\$2").WithArgs("current-app", "opaque/group+id").WillReturnResult(sqlmock.NewResult(0, 1))
			}
			h := NewQQBotHandler(qqbot.New(db, qqTestEncryptor{}, true, nil))
			r := gin.New()
			if role != "" {
				r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), role); c.Next() })
			}
			r.DELETE("/groups", h.DeleteDiscoveredGroup)
			w := httptest.NewRecorder()
			req := httptest.NewRequest("DELETE", "/groups", strings.NewReader(`{"group_id":"opaque/group+id"}`))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			if role == "admin" {
				require.Equal(t, http.StatusOK, w.Code)
			} else {
				require.Equal(t, http.StatusForbidden, w.Code)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestQQBotDeleteDiscoveredGroupRejectsInvalidID(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	h := NewQQBotHandler(qqbot.New(db, qqTestEncryptor{}, true, nil))
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), "admin"); c.Next() })
	r.DELETE("/groups", h.DeleteDiscoveredGroup)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/groups", strings.NewReader(`{"group_id":"bad id"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQQBotWebhookRejectsUnsignedAndAlteredEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, app string
		valid     bool
		alter     bool
		want      int
		eventType string
	}{
		{"unsigned", "app", false, false, 401, "UNUSED"}, {"wrong app", "wrong", true, false, 401, "UNUSED"}, {"altered", "app", true, true, 401, "UNUSED"}, {"valid ignored event", "app", true, false, 200, "UNUSED"},
		{"unsigned button", "app", false, false, 401, "INTERACTION_CREATE"}, {"wrong button app", "wrong", true, false, 401, "INTERACTION_CREATE"}, {"altered button", "app", true, true, 401, "INTERACTION_CREATE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			cfg := qqbot.DefaultConfig()
			cfg.AppID = "app"
			cfg.AppSecret = "test-secret"
			rawConfig, err := json.Marshal(cfg)
			require.NoError(t, err)
			mock.ExpectQuery("SELECT value FROM settings").WithArgs("qq_bot_config").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(string(rawConfig)))
			h := NewQQBotHandler(qqbot.New(db, qqTestEncryptor{}, true, nil))
			r := gin.New()
			r.POST("/hook", h.Webhook)
			raw := []byte(strings.Replace(`{"op":0,"t":"UNUSED","d":{}}`, "UNUSED", tc.eventType, 1))
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			seed := "test-secret"
			for len(seed) < 32 {
				seed += seed
			}
			key := ed25519.NewKeyFromSeed([]byte(seed[:32]))
			sig := hex.EncodeToString(ed25519.Sign(key, append([]byte(ts), raw...)))
			if tc.alter {
				raw = append(raw, ' ')
			}
			req := httptest.NewRequest("POST", "/hook", bytes.NewReader(raw))
			req.Header.Set("X-Bot-Appid", tc.app)
			if tc.valid {
				req.Header.Set("X-Signature-Timestamp", ts)
				req.Header.Set("X-Signature-Ed25519", sig)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, tc.want, w.Code)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
func TestQQBotWebhookChallengeAndBodyLimit(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	cfg := qqbot.DefaultConfig()
	cfg.AppID = "app"
	cfg.AppSecret = "test-secret"
	raw, _ := json.Marshal(cfg)
	mock.ExpectQuery("SELECT value FROM settings").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(string(raw)))
	h := NewQQBotHandler(qqbot.New(db, qqTestEncryptor{}, true, nil))
	r := gin.New()
	r.POST("/hook", h.Webhook)
	req := httptest.NewRequest("POST", "/hook", strings.NewReader(`{"op":13,"d":{"plain_token":"challenge","event_ts":"123"}}`))
	req.Header.Set("X-Bot-Appid", "app")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "signature")
	require.NotContains(t, w.Body.String(), "test-secret")
	req = httptest.NewRequest("POST", "/hook", strings.NewReader(strings.Repeat("x", (2<<20)+1)))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQQBotConfigDoesNotReturnSecrets(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	cfg := qqbot.DefaultConfig()
	cfg.APIKey = "secret-key"
	cfg.AppSecret = "qq-secret"
	raw, _ := json.Marshal(cfg)
	mock.ExpectQuery("SELECT value FROM settings").WithArgs("qq_bot_config").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(string(raw)))
	bot := qqbot.New(db, qqTestEncryptor{}, true, nil)
	h := NewQQBotHandler(bot)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), "admin"); c.Next() })
	r.GET("/config", h.GetConfig)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), "GET", "/config", nil))
	require.Equal(t, 200, w.Code)
	require.NotContains(t, w.Body.String(), "secret-key")
	require.NotContains(t, w.Body.String(), "qq-secret")
	require.Contains(t, w.Body.String(), `"api_key_set":true`)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQQBotSaveConfigAcceptsFullCustomCommandList(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	cfg := qqbot.DefaultConfig()
	old, err := json.Marshal(cfg)
	require.NoError(t, err)
	version := qqbot.ConfigVersion(cfg)
	cfg.HistoryMessages = 25
	for i := 0; i < 20; i++ {
		cfg.CustomCommands = append(cfg.CustomCommands, qqbot.CustomCommand{
			Command: "/a" + strconv.Itoa(i), Reply: strings.Repeat("字", 2000), Enabled: true,
		})
	}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.Greater(t, len(raw), 64<<10, "full valid configurations exceed the previous body limit")
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT value FROM settings").WithArgs("qq_bot_config").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(string(old)))
	mock.ExpectExec("INSERT INTO settings").WithArgs("qq_bot_config", string(raw)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	h := NewQQBotHandler(qqbot.New(db, qqTestEncryptor{}, true, nil))
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), "admin"); c.Next() })
	r.PUT("/config", h.SaveConfig)
	req := httptest.NewRequest(http.MethodPut, "/config", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", "\""+version+"\"")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var result struct {
		Data qqbot.Config `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Equal(t, cfg.CustomCommands, result.Data.CustomCommands)
	require.Equal(t, 25, result.Data.HistoryMessages)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQQBotEmailBindingPreviewWithoutLoginAndNoTokenEcho(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	cfg := qqbot.DefaultConfig()
	cfg.Enabled = true
	cfg.AppID = "app"
	cfg.AllowedGroups = []string{"group"}
	raw, _ := json.Marshal(cfg)
	mock.ExpectQuery("SELECT value FROM settings").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(string(raw)))
	mock.ExpectQuery("SELECT r.member_id,r.group_id,r.nickname,r.expires_at").WillReturnRows(sqlmock.NewRows([]string{"member_id", "group_id", "nickname", "expires_at"}).AddRow("member", "group", "Alice", time.Now().Add(10*time.Minute)))
	h := NewQQBotHandler(qqbot.New(db, qqTestEncryptor{}, true, nil))
	r := gin.New()
	r.POST("/preview", h.PreviewEmailBinding)
	r.POST("/confirm", h.ConfirmEmailBinding)
	token := strings.Repeat("a", 48)
	body, err := json.Marshal(map[string]string{"token": token})
	require.NoError(t, err)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/preview", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Contains(t, w.Body.String(), "Alice")
	require.NotContains(t, w.Body.String(), token)
	// A missing identity cannot be confirmed, even with a syntactically valid token.
	w = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/confirm", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, 400, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}
