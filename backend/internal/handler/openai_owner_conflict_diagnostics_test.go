//go:build unit && ownerdiagnostics

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOwnerConflictPersistenceKeepsTurnAndSafeSubreason(t *testing.T) {
	for _, tc := range []struct{ detail, want string }{{"tool_unpaired", "tool_unpaired"}, {"shadow_epoch_changed", "shadow_epoch_changed"}, {"credential-and-body-canary", "unknown"}, {"", "unknown"}} {
		t.Run(tc.want+tc.detail, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 4)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			service.SetOpenAIClientTransport(c, service.OpenAIClientTransportWS)
			service.BeginOpsStreamTurn(c, 53)
			written := false
			failure := &service.OpsRecoveryFailure{Protocol: "websocket", Phase: "before_dispatch", RequestWritten: &written,
				Controls: &service.OpsRecoveryControls{Observation: "complete", Snapshot: "oauth_replay", Tools: "changed", ToolChoice: "match", ParallelToolCalls: "match", Instructions: "match", DigestChanged: true}}
			// Baseline RED compiles this independent fixture but does not run it;
			// old production ignores the new diagnostic field during decoding.
			encoded, err := json.Marshal(map[string]string{"Subreason": tc.detail})
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(encoded, failure))
			service.MarkOpsStreamErrorValue(c, service.OpsStreamError{ErrType: "invalid_request_error", Code: "encrypted_agent_recovery_owner_conflict", Message: "synthetic refusal", IntendedStatus: 409, CountTowardsSLA: true, RecoveryFailure: failure})
			repo := &ingressRejectOpsRepo{}
			ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			logOpsStreamError(c, ops, http.StatusSwitchingProtocols)
			require.Equal(t, int64(1), OpsErrorLogQueueLength())
			flushOpsErrorLogBatch([]opsErrorLogJob{<-opsErrorLogQueue})
			require.Len(t, repo.entries, 1)
			body := repo.entries[0].ErrorBody
			require.Equal(t, tc.want, gjson.Get(body, "error.subreason").String())
			require.EqualValues(t, 53, gjson.Get(body, "error.turn").Int())
			require.EqualValues(t, 101, gjson.Get(body, "error.wire_status").Int())
			require.Equal(t, gjson.False, gjson.Get(body, "error.request_written").Type)
			if tc.detail == "tool_unpaired" {
				require.Equal(t, "changed", gjson.Get(body, "error.controls.tools").String())
				require.Equal(t, "oauth_replay", gjson.Get(body, "error.controls.snapshot").String())
			}
			require.NotContains(t, body, "credential-and-body-canary")
		})
	}
}
