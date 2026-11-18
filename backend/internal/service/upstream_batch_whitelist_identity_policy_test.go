//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBatchWhitelistNativePolicyRejectsTaskLifecycle(t *testing.T) {
	for _, task := range []string{"", "existing-task"} {
		t.Run(task, func(t *testing.T) {
			key, privateKey := newTestAgentIdentityKey(t)
			account := newCodexModelsTestAccount()
			account.Credentials["auth_mode"] = OpenAIAuthModeAgentIdentity
			account.Credentials["agent_runtime_id"] = key.runtimeID
			account.Credentials["agent_private_key"] = privateKey
			account.Credentials["task_id"] = task
			repo := &batchWhitelistReadOnlyRepo{upstreamModelMappingRepoStub: &upstreamModelMappingRepoStub{account: account}}
			gateway := newCodexModels401TestService(repo)
			gateway.accountRepo = repo
			_, err := gateway.fetchNativeOpenAIModelsListWithPolicy(context.Background(), account, openAIModelsReadWhitelist)
			require.Error(t, err, "read-only discovery cannot create or recover an identity task")
			require.Zero(t, repo.credentialWrites.Load())
			require.Equal(t, privateKey, account.GetCredential("agent_private_key"))
			require.Equal(t, task, account.GetCredential("task_id"))
		})
	}
}

func TestBatchWhitelistSyncAgentIdentityTaskLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, task string
		whitelist  bool
	}{
		{"whitelist without task", "", true},
		{"whitelist with task", "existing-task", true},
		{"legacy discovery still registers task", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, privateKey := newTestAgentIdentityKey(t)
			account := newCodexModelsTestAccount()
			account.Status, account.Schedulable = StatusActive, true
			account.Credentials["auth_mode"] = OpenAIAuthModeAgentIdentity
			account.Credentials["agent_runtime_id"] = key.runtimeID
			account.Credentials["agent_private_key"] = privateKey
			account.Credentials["task_id"] = tc.task
			account.Credentials["model_mapping"] = map[string]any{"old": "old"}
			require.False(t, account.IsExcelBPSEnabled(), "exercise the non-BPS sync entry")
			before, err := json.Marshal(account)
			require.NoError(t, err)
			var registrations atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				registrations.Add(1)
				_, _ = io.WriteString(w, `{"task_id":"registered-task"}`)
			}))
			original := openAIAgentIdentityAuthAPIBaseURL
			openAIAgentIdentityAuthAPIBaseURL = server.URL
			t.Cleanup(func() { openAIAgentIdentityAuthAPIBaseURL = original; server.Close() })
			repo := &batchWhitelistReadOnlyRepo{upstreamModelMappingRepoStub: &upstreamModelMappingRepoStub{account: account}}
			ws := &agentIdentityWSInvalidationRecorder{}
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusServiceUnavailable, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"error":"synthetic catalog failure"}`)),
			}}
			svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream, cfg: upstreamModelSyncTestConfig(), agentIdentityWS: ws}
			options := UpstreamModelSyncOptions{}
			if tc.whitelist {
				options = whitelistOnlyOptions(t)
			}
			_, err = svc.SyncUpstreamModelCatalogWithOptions(context.Background(), account, options)
			require.Error(t, err)
			if tc.whitelist {
				var syncErr *UpstreamModelSyncError
				require.True(t, errors.As(err, &syncErr))
				require.Equal(t, UpstreamModelSyncErrorUnsupported, syncErr.Kind)
				require.Zero(t, registrations.Load(), "whitelist sync must reject before task registration")
				require.Zero(t, repo.credentialWrites.Load(), "whitelist sync must not persist a task")
				require.Empty(t, ws.accountIDs)
				require.Empty(t, upstream.requests)
				after, marshalErr := json.Marshal(account)
				require.NoError(t, marshalErr)
				require.JSONEq(t, string(before), string(after))
			} else {
				// Legacy discovery retains task setup even if its subsequent
				// model-list request fails; the guard is whitelist-only.
				require.EqualValues(t, 1, registrations.Load())
				require.EqualValues(t, 1, repo.credentialWrites.Load())
				require.Equal(t, "registered-task", account.GetCredential("task_id"))
				require.Equal(t, []int64{account.ID}, ws.accountIDs)
				require.Len(t, upstream.requests, 1)
			}
			require.Zero(t, repo.authErrors.Load())
			require.Zero(t, repo.tempUnsched.Load())
			require.Zero(t, repo.extraWrites.Load())
			require.Zero(t, repo.applyCalls)
			require.Zero(t, repo.claimCalls)
		})
	}
}
