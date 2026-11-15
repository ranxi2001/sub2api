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

func TestQQBotMemberDirectoryRequiresAdminAndValidFilters(t *testing.T) {
	for _, role := range []string{"", "user", "admin"} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		h := NewQQBotHandler(qqbot.New(db, qqTestEncryptor{}, true, nil))
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), role); c.Next() })
		r.GET("/members", h.ListMembers)
		paths := []string{"/members?page=0", "/members?page=bad", "/members?page_size=101", "/members?member_id=chosen", "/members?group_id=bad%20group"}
		if role != "admin" {
			paths = append(paths, "/members", "/members?group_id=group")
		}
		for _, path := range paths {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			want := http.StatusForbidden
			if role == "admin" {
				want = http.StatusBadRequest
			}
			require.Equal(t, want, w.Code, path)
		}
		require.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	}
}

func TestQQBotMemberDirectoryChecksApplicationAndDisablesCaching(t *testing.T) {
	for _, mismatched := range []bool{false, true} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		cfg := qqbot.DefaultConfig()
		if mismatched {
			cfg.AppID = "current-app"
		}
		raw, err := json.Marshal(cfg)
		require.NoError(t, err)
		mock.ExpectBegin()
		mock.ExpectExec("SELECT pg_advisory_xact_lock_shared").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("SELECT value FROM settings").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(string(raw)))
		if mismatched {
			mock.ExpectRollback()
		} else {
			mock.ExpectCommit()
		}
		h := NewQQBotHandler(qqbot.New(db, qqTestEncryptor{}, true, nil))
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), "admin"); c.Next() })
		r.GET("/members", h.ListMembers)
		path := "/members"
		if mismatched {
			path += "?app_id=stale-app"
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if mismatched {
			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Contains(t, w.Body.String(), "AppID")
		} else {
			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			require.Contains(t, w.Body.String(), `"items":[]`)
		}
		require.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	}
}
