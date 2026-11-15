package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestQQRewardCodesRequireAdminAndValidateBeforeWrites(t *testing.T) {
	for _, role := range []string{"", "user", "admin"} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		h := NewQQBotHandler(qqbot.New(db, qqTestEncryptor{}, true, nil))
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), role); c.Next() })
		r.GET("/codes", h.ListRewardCodes)
		r.PATCH("/codes/:id", h.UpdateRewardCode)
		for _, tc := range []struct{ method, path, body string }{
			{"GET", "/codes?page=0", ""}, {"GET", "/codes?status=anything", ""},
			{"PATCH", "/codes/0", `{"action":"disable"}`}, {"PATCH", "/codes/1", `{"action":"delete"}`},
		} {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			want := 403
			if role == "admin" {
				want = 400
			}
			require.Equal(t, want, w.Code)
		}
		require.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	}
}
