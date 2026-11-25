//go:build integration

package repository_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOAuthImportSchedulingHTTP(t *testing.T) {
	db, client := repository.UpstreamChannelsTestDatabase(t)
	cfg := &config.Config{}
	cfg.Runtime.Role = config.RuntimeRoleGateway
	settings := repository.NewSettingRepository(client)
	groups := repository.NewAdminGroupRepository(client, db)
	accounts := repository.NewAdminAccountRepository(client, db, nil)
	svc := service.NewAdminService(cfg, nil, groups, accounts, nil, nil, nil, nil, nil, nil, nil, nil, nil, client, service.NewSettingService(settings, cfg), nil, nil, nil, nil, nil, nil, nil, nil)
	ah := admin.NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ops := service.ProvideAccountOpsService(settings, repository.NewAccountOpsRepository(db), nil, nil, groups, cfg, nil, nil, nil, nil, nil)
	t.Cleanup(ops.Stop)
	oh := admin.NewAccountOpsHandler(ops, nil)
	router := gin.New()
	router.PUT("/config", oh.SaveAutoConfig)
	router.GET("/config", oh.GetAutoConfig)
	router.POST("/import", ah.ImportCodexSession)
	request := func(method, path string, body any, dest any) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			reader = strings.NewReader(string(raw))
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		require.Equal(t, 200, w.Code, w.Body.String())
		var envelope struct{ Data json.RawMessage }
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
		require.NoError(t, json.Unmarshal(envelope.Data, dest))
	}
	var groupID int64
	require.NoError(t, db.QueryRow("INSERT INTO groups(name,platform,status) VALUES('initial-schedule','openai','active') RETURNING id").Scan(&groupID))
	var saved service.OAuthAutoConfig
	for _, jsonOn := range []bool{true, false} {
		for _, twoFAOn := range []bool{true, false} {
			request("PUT", "/config", map[string]any{"enabled": true, "group_ids": []int64{groupID}, "concurrency": 8, "json_import_auto_schedule": map[string]bool{"openai": jsonOn}, "two_fa_login_auto_schedule": map[string]bool{"openai": twoFAOn}}, &saved)
			request("GET", "/config", nil, &saved)
			require.Equal(t, map[string]bool{"openai": jsonOn}, saved.JSONImportAutoSchedule)
			require.Equal(t, map[string]bool{"openai": twoFAOn}, saved.TwoFALoginAutoSchedule)
			for _, twoFA := range []bool{false, true} {
				name := fmt.Sprintf("fixture-%t-%t-%t", jsonOn, twoFAOn, twoFA)
				content, err := json.Marshal(map[string]any{"access_token": "access-" + name, "refresh_token": "refresh-" + name, "schedulable": true})
				require.NoError(t, err)
				input := map[string]any{"name": name, "content": string(content), "initialize_openai_two_fa": twoFA, "update_existing": false, "skip_existing": true}
				var result admin.CodexSessionImportResult
				request("POST", "/import", input, &result)
				require.Equal(t, 1, result.Created, "%+v", result.Errors)
				id := result.Items[0].AccountID
				var schedule bool
				var concurrency int
				var credential string
				require.NoError(t, db.QueryRow("SELECT schedulable,concurrency,credentials->>'access_token' FROM accounts WHERE id=$1", id).Scan(&schedule, &concurrency, &credential))
				want := jsonOn
				if twoFA {
					want = twoFAOn
				}
				require.Equal(t, want, schedule)
				require.Equal(t, 8, concurrency)
				require.Equal(t, "access-"+name, credential)
				_, err = db.Exec("UPDATE accounts SET schedulable=$2 WHERE id=$1", id, !want)
				require.NoError(t, err)
				request("POST", "/import", input, &result)
				require.Equal(t, 1, result.Skipped)
				require.NoError(t, db.QueryRow("SELECT schedulable FROM accounts WHERE id=$1", id).Scan(&schedule))
				require.Equal(t, !want, schedule)
			}
		}
	}
}
