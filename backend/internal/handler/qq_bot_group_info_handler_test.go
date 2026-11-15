package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestQQBotGroupInfoAdminAndScope(t *testing.T) {
	for _, tc := range []struct {
		name, role, app, group string
		known                  bool
		want                   int
	}{
		{"anonymous", "", "app", "allowed", false, http.StatusForbidden},
		{"user", "user", "app", "allowed", false, http.StatusForbidden},
		{"invalid group", "admin", "app", "bad%20group", false, http.StatusBadRequest},
		{"stale app", "admin", "old", "allowed", false, http.StatusBadRequest},
		{"missing app", "admin", "", "allowed", false, http.StatusBadRequest},
		{"allowed group", "admin", "app", "allowed", false, http.StatusOK},
		{"unknown group", "admin", "app", "discovered", false, http.StatusBadRequest},
		{"discovered group", "admin", "app", "discovered", true, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			if tc.role == "admin" && tc.group != "bad%20group" {
				cfg := qqbot.DefaultConfig()
				cfg.AppID = "app"
				cfg.AllowedGroups = []string{"allowed"}
				raw, err := json.Marshal(cfg)
				require.NoError(t, err)
				mock.ExpectQuery("SELECT value FROM settings").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(string(raw)))
				if tc.app == "app" && tc.group == "discovered" {
					mock.ExpectQuery("SELECT EXISTS").WithArgs("app", "discovered").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(tc.known))
				}
			}
			h := NewQQBotHandler(qqbot.New(db, qqTestEncryptor{}, true, nil))
			r := gin.New()
			r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), tc.role); c.Next() })
			r.GET("/groups/:group_id/info", h.GroupInfo)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/groups/"+tc.group+"/info?app_id="+tc.app, nil))
			require.Equal(t, tc.want, w.Code, w.Body.String())
			if tc.want == http.StatusOK {
				require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
				require.Contains(t, w.Body.String(), `"status":"unconfigured"`)
				require.Contains(t, w.Body.String(), `"app_id":"app"`)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
