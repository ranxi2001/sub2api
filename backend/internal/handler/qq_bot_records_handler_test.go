package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestQQActivityRecordAccessAndValidation(t *testing.T) {
	for _, role := range []string{"", "user", "admin"} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		h := NewQQBotHandler(qqbot.New(db, qqTestEncryptor{}, true, nil))
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), role); c.Next() })
		r.GET("/records/:kind", h.ListActivityRecords)
		paths := []string{"/records/unknown", "/records/invite?page=0", "/records/checkin?page=bad", "/records/lottery?page_size=101", "/records/invite?page_size=-1"}
		if role != "admin" {
			paths = append(paths, "/records/checkin", "/records/lottery", "/records/invite")
		}
		for _, path := range paths {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			want := 403
			if role == "admin" {
				want = 400
			}
			require.Equal(t, want, w.Code, path)
		}
		require.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	}
}

func TestQQActivityRecordResponseIsNotCached(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	cfg := qqbot.DefaultConfig()
	cfg.AppID = "app"
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	mock.ExpectQuery("SELECT value FROM settings").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(string(raw)))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COUNT").WithArgs("app", "name").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT r.id::text").WithArgs("app", "name", 20, 0).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectCommit()
	h := NewQQBotHandler(qqbot.New(db, qqTestEncryptor{}, true, nil))
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), "admin"); c.Next() })
	r.GET("/records/:kind", h.ListActivityRecords)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/records/checkin?search=%20name%20", nil))
	require.Equal(t, 200, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Contains(t, w.Body.String(), `"items":[]`)
	require.NoError(t, mock.ExpectationsWereMet())
}
