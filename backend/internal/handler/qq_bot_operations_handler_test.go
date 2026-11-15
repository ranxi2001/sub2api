package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestQQBotOperationsRequireAdmin(t *testing.T) {
	for _, role := range []string{"", "user"} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		h := NewQQBotHandler(qqbot.New(db, qqTestEncryptor{}, true, nil))
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), role); c.Next() })
		r.PUT("/config/:section", h.SaveConfigSection)
		for path, handler := range map[string]gin.HandlerFunc{
			"/history": h.ConfigHistory, "/overview": h.Overview, "/jobs": h.ListJobs,
			"/diagnosis": h.Diagnose, "/commands": h.Commands, "/preview": h.PreviewActivities,
			"/transfers": h.TransferHistory, "/moderation": h.ListModerationRecords,
		} {
			r.GET(path, handler)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			require.Equal(t, http.StatusForbidden, w.Code, path)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/config/chat", strings.NewReader(`{}`)))
		require.Equal(t, http.StatusForbidden, w.Code)
		require.NoError(t, mock.ExpectationsWereMet(), "unauthorized requests must not access the database")
		_ = db.Close()
	}
}

func TestQQBotSaveConflictsReturn409WithoutWriting(t *testing.T) {
	for _, kind := range []string{"module", "basic", "invite", "transfer", "checkin", "lottery", "ranking"} {
		for _, version := range []string{"", `"stale"`} {
			t.Run(kind+"/"+version, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				require.NoError(t, err)
				defer func() { _ = db.Close() }()
				cfg := qqbot.DefaultConfig()
				cfg.AppID = "app"
				raw, err := json.Marshal(cfg)
				require.NoError(t, err)
				mock.ExpectBegin()
				mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
				if kind != "lottery" {
					mock.ExpectQuery("SELECT value FROM settings").WithArgs("qq_bot_config").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(string(raw)))
				}
				if kind == "ranking" {
					mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
				}
				if kind == "checkin" || kind == "lottery" || kind == "ranking" {
					mock.ExpectQuery("SELECT value FROM settings").WillReturnRows(sqlmock.NewRows([]string{"value"}))
				}
				mock.ExpectRollback()
				h := NewQQBotHandler(qqbot.New(db, qqTestEncryptor{}, true, nil))
				handlers := map[string]gin.HandlerFunc{"module": h.SaveConfigSection, "basic": h.SaveConfig, "invite": h.SaveInviteConfig, "transfer": h.SaveTransferConfig, "checkin": h.SaveCheckinConfig, "lottery": h.SaveLotteryConfig, "ranking": h.SaveRankingConfig}
				body := `{}`
				switch kind {
				case "module":
					b, _ := json.Marshal(map[string]any{"version": version, "config": map[string]any{}})
					body = string(b)
				case "lottery":
					body = `{"title":"Test","recharge_step":10,"chances_per_step":1,"prizes":[{"name":"Test","amount":1,"probability_bps":10000}]}`
				}
				r := gin.New()
				r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), "admin"); c.Next() })
				r.PUT("/config/:section", handlers[kind])
				req := httptest.NewRequest(http.MethodPut, "/config/chat", strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("If-Match", version)
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
				require.NoError(t, mock.ExpectationsWereMet())
			})
		}
	}
}
