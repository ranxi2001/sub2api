//go:build unit && issue1077

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/regional/ledger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIssue1077RegionalFinalizerOnlyCertifiedUnsentHandshake(t *testing.T) {
	for _, tc := range []struct {
		name                                  string
		sent, canceled, wrongAttempt, generic bool
	}{
		{name: "canceled_unsent", canceled: true},
		{name: "cancel_unknown"},
		{name: "sent", sent: true, canceled: true},
		{name: "another_attempt", canceled: true, wrongAttempt: true},
		{name: "generic_error", canceled: true, generic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &regionalMeterAttempt{}
			a.sent.Store(tc.sent)
			a.canceledBeforeStart.Store(tc.canceled)
			ctx := context.WithValue(context.Background(), regionalMeterKey{}, a)
			failure := &UpstreamFailoverError{StatusCode: 401}
			certified := &openAIWSHandshakeAccountFailover{cause: failure, meter: a}
			if tc.wrongAttempt {
				certified.meter = &regionalMeterAttempt{}
			}
			var forwardErr error = certified
			if tc.generic {
				forwardErr = failure
			}
			err := regionalMeterFinishForward(ctx, nil, forwardErr, func(*OpenAIForwardResult, error) error { return nil })
			var replay *UpstreamFailoverError
			require.Equal(t, tc.canceled && !tc.sent && !tc.wrongAttempt && !tc.generic, errors.As(err, &replay))
		})
	}
}

func TestIssue1077RegionalReplacementRequiresFreshBoundIdentity(t *testing.T) {
	for _, mode := range []string{"fresh_B", "reused_A_ID", "changed_payload", "start_unknown"} {
		t.Run(mode, func(t *testing.T) {
			body := []byte(`{"model":"mock-text","input":"hello","max_output_tokens":10}`)
			digest := sha256.Sum256(body)
			retired := &regionalMeterAttempt{reservation: ledger.Reservation{RequestID: "retired-A", UserID: 5, APIKeyID: 4, AccountID: 1, Model: "mock-text", PayloadSHA256: hex.EncodeToString(digest[:])}}
			retired.canceledBeforeStart.Store(true)
			if mode == "reused_A_ID" {
				retired.reservation.RequestID = "meter-1"
			}
			if mode == "changed_payload" {
				retired.reservation.PayloadSHA256 = "different"
			}
			if mode == "start_unknown" {
				retired.sent.Store(true)
			}
			ctx := context.WithValue(context.Background(), regionalMeterKey{}, retired)
			l := &meterTestLedger{}
			s := &OpenAIGatewayService{regionalMetering: testRegionalMeter(l)}
			c := newOpenAIRejectedFieldTestContext(body)
			c.Set("api_key", &APIKey{ID: 4, UserID: 5})
			account := newOpenAIRejectedFieldTestAccount()
			account.ID = 2
			next, _, err := s.beginRegionalMeter(ctx, c, account, body)
			switch mode {
			case "fresh_B":
				require.NoError(t, err)
				a := regionalMeterAttemptFrom(next)
				require.NotSame(t, retired, a)
				require.Equal(t, int64(2), a.reservation.AccountID)
				require.NotEqual(t, retired.reservation.RequestID, a.reservation.RequestID)
				require.Equal(t, retired.reservation.PayloadSHA256, a.reservation.PayloadSHA256)
				require.Equal(t, 1, l.reservations)
			case "start_unknown":
				require.NoError(t, err)
				require.Same(t, retired, regionalMeterAttemptFrom(next))
				require.Zero(t, l.reservations)
				require.Error(t, regionalMeterBeforeSend(next))
			default:
				require.Error(t, err)
				require.Zero(t, l.reservations)
			}
		})
	}
}

func TestIssue1077HandshakeContextGuards(t *testing.T) {
	for _, mode := range []string{"owner", "no_replay", "anchor", "turn_state", "cached_turn_state", "cancel", "committed"} {
		t.Run(mode, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header.Set("Session-Id", "fixture-bound-session")
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			d := &wsSSETestDialer{status: 401, err: &openAIWSHandshakeError{Body: []byte(`{"error":{"code":"invalid_api_key"}}`), Err: errors.New("fixture handshake refusal")}}
			s := wsSSETestService(t, d, wsSSEGuardHTTPUpstream())
			ctx := context.Background()
			switch mode {
			case "owner":
				ctx = context.WithValue(ctx, openAIAgentRecoveryContextKey{}, &openAIAgentRecoveryRequest{owner: &openAIAgentRecoveryBinding{accountID: 1}})
			case "no_replay":
				ctx = context.WithValue(ctx, openAIAgentRecoveryContextKey{}, &openAIAgentRecoveryRequest{omittedHistory: true})
			case "anchor":
				ctx = context.WithValue(ctx, codexWSAnchorContextKey{}, &codexWSAnchorTurn{expires: time.Now().Add(time.Minute)})
			case "turn_state":
				c.Request.Header.Set(openAIWSTurnStateHeader, "fixture-bound-state")
			case "cached_turn_state":
				// The HTTP forwarder shares the WS thread scope, not the legacy
				// session hash. Seed the binding that this request actually reads.
				scope, _ := resolveOpenAIWSExecutionScope(c, []byte(wsSSETestRequest), getAPIKeyIDFromContext(c))
				require.NotEmpty(t, scope)
				store := s.getOpenAIWSStateStore()
				store.BindSessionTurnState(0, scope, "fixture-cached-state", time.Minute)
				state, found := store.GetSessionTurnState(0, scope)
				require.True(t, found)
				require.Equal(t, "fixture-cached-state", state)
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "committed":
				c.Writer.WriteHeaderNow()
			}
			c.Request = c.Request.WithContext(ctx)
			_, err := s.Forward(ctx, c, wsSSETestAccount(), []byte(wsSSETestRequest))
			var replay *UpstreamFailoverError
			require.False(t, errors.As(err, &replay), "context guard must survive the actual Forward boundary")
		})
	}
}
