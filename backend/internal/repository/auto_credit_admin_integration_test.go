//go:build vipcreditintegration

package repository_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func adminCreditDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("VIP_CREDIT_TEST_DSN")
	require.NotEmpty(t, dsn)
	db, e := sql.Open("postgres", dsn)
	require.NoError(t, e)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, repository.ApplyMigrations(context.Background(), db))
	return db
}
func adminCreditUser(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var id int64
	require.NoError(t, db.QueryRow(`INSERT INTO users(email,password_hash,balance) VALUES($1,'test-only',0) RETURNING id`, fmt.Sprintf("vip-admin-%d@example.invalid", time.Now().UnixNano())).Scan(&id))
	return id
}
func TestVipCreditAdminAPIWithRealRepository(t *testing.T) {
	db := adminCreditDB(t)
	uid := adminCreditUser(t, db)
	h := &adminhandler.UserHandler{}
	h.SetAutoCredit(repository.NewAutoCreditRepository(db))
	role := service.RoleUser
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: uid})
		c.Set(string(middleware.ContextKeyUserRole), role)
		c.Next()
	})
	engine.GET("/users/:id/auto-credit", h.GetAutoCredit)
	engine.PUT("/users/:id/auto-credit", h.SaveAutoCredit)
	engine.POST("/users/:id/auto-credit/:entry_id/settle", h.SettleAutoCredit)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(w, r)
		return w
	}
	path := fmt.Sprintf("/users/%d/auto-credit", uid)
	require.Equal(t, 403, call("GET", path, "").Code)
	require.Equal(t, 403, call("PUT", path, `{}`).Code)
	role = service.RoleAdmin
	require.Equal(t, 400, call("PUT", path, `{"enabled":true}`).Code)
	require.Equal(t, 400, call("PUT", path, `{"version":0,"threshold":"0"}`).Code)
	saved := call("PUT", path, `{"version":0,"enabled":true,"threshold":"1","amount":"5.00000001","cap":"10.00000002"}`)
	require.Equal(t, 200, saved.Code, saved.Body.String())
	require.Equal(t, 409, call("PUT", path, `{"version":0,"enabled":false}`).Code)
	_, err := repository.NewAutoCreditRepository(db).Evaluate(context.Background(), uid)
	require.NoError(t, err)
	got := call("GET", path+"?status=pending", "")
	require.Equal(t, 200, got.Code)
	var envelope struct {
		Data service.AutoCreditState `json:"data"`
	}
	require.NoError(t, json.Unmarshal(got.Body.Bytes(), &envelope))
	require.Equal(t, "5.00000001", envelope.Data.Outstanding)
	require.Len(t, envelope.Data.Entries, 1)
	settle := fmt.Sprintf("%s/%d/settle", path, envelope.Data.Entries[0].ID)
	require.Equal(t, 400, call("POST", settle, `{"confirm":false}`).Code)
	require.Equal(t, 200, call("POST", settle, `{"confirm":true}`).Code)
	require.Equal(t, 200, call("POST", settle, `{"confirm":true}`).Code)
	var balance string
	require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, "5.00000001", balance)
}
