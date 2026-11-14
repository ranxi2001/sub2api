package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMatureQQProbeRequiresExplicitConfirmation(t *testing.T) {
	for _, kind := range []string{"legacy", "setup"} {
		t.Run(kind, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			// A deterministic disabled configuration; an unknown capability cannot
			// contact a model even in the old implementation.
			mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"value"}))
			h := NewQQBotHandler(qqbot.New(db, nil, false, nil))
			r := gin.New()
			r.Use(func(c *gin.Context) { c.Set("user_role", "admin"); c.Next() })
			if kind == "legacy" {
				r.POST("/probe/:kind", h.Probe)
			} else {
				r.POST("/probe/:kind", h.SetupProbe)
			}
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/probe/unknown", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			require.Equal(t, 400, w.Code)
			require.Contains(t, w.Body.String(), "QQ_BOT_CONFIRMATION_REQUIRED", "capability probes must require explicit server-side confirmation")
		})
	}
}
