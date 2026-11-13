//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type omit400SettingsRepo struct {
	service.SettingRepository
	enabled400, enabled409 bool
}

func (s *omit400SettingsRepo) GetValue(_ context.Context, key string) (string, error) {
	switch key {
	case service.SettingKeyOpenAI400OmitEncryptedHistoryEnabled:
		return fmt.Sprint(s.enabled400), nil
	case service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled:
		return fmt.Sprint(s.enabled409), nil
	default:
		return "", service.ErrSettingNotFound
	}
}

func (s *omit400SettingsRepo) GetMultiple(_ context.Context, _ []string) (map[string]string, error) {
	return map[string]string{
		service.SettingKeyOpenAI400OmitEncryptedHistoryEnabled: fmt.Sprint(s.enabled400),
		service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: fmt.Sprint(s.enabled409),
	}, nil
}

// Actual handler, independent Redis clients, actual localhost HTTP requests.
// The provider checks preserved readable history and rejects remaining fixture
// ciphertext. This is an offline contract test, not provider compatibility proof.
func TestOpenAI400OmitHistoryHTTP(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, scenario := range []string{"standalone", "after409", "400_off", "still_rejected", "retry_503", "retry_disconnect"} {
			t.Run(fmt.Sprintf("%s/stream=%t", scenario, stream), func(t *testing.T) {
				clients := issue805Redis(t)
				followup := issue715Body(true, stream)
				var err error
				followup, err = sjson.SetRaw(followup, "input.5", gjson.Get(followup, "input.4").Raw)
				require.NoError(t, err)
				followup, err = sjson.SetRaw(followup, "input.4", `{"type":"reasoning","encrypted_content":"fixture-400-reasoning","summary":[{"type":"summary_text","text":"visible summary survives"}]}`)
				require.NoError(t, err)
				var mu sync.Mutex
				var bodies [][]byte
				var identities []string
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, readErr := io.ReadAll(r.Body)
					if readErr != nil {
						t.Error(readErr)
						return
					}
					mu.Lock()
					bodies = append(bodies, body)
					identities = append(identities, r.Header.Get("Authorization"))
					n := len(bodies)
					mu.Unlock()
					if n >= 3 && scenario == "retry_503" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = io.WriteString(w, `{"error":{"message":"fixture unavailable"}}`)
						return
					}
					if n >= 3 && scenario == "retry_disconnect" {
						hijacker, ok := w.(http.Hijacker)
						if !ok {
							t.Error("HTTP fixture must support connection close")
							return
						}
						conn, _, err := hijacker.Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
						return
					}
					if n > 1 && (strings.Contains(string(body), "fixture-400-reasoning") || scenario == "still_rejected") {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"error":{"code":"invalid_encrypted_content","message":"The encrypted content could not be verified."}}`)
						return
					}
					if n > 1 {
						for _, path := range []string{"input.1", "input.2", "input.3", "input.5"} {
							require.JSONEq(t, gjson.Get(followup, path).Raw, gjson.GetBytes(body, path).Raw, path)
						}
						require.Contains(t, string(body), "visible summary survives")
						require.NotContains(t, string(body), "fixture-task-reference-715")
					}
					output := "[" + issue715Agent + "]"
					if n > 1 {
						output = `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"continued from readable history"}]}]`
					}
					payload := fmt.Sprintf(`{"id":"resp_400_%d","status":"completed","error":null,"model":"gpt-5.1","output":%s,"usage":{"input_tokens":5,"output_tokens":3}}`, n, output)
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\ndata: [DONE]\n\n", payload)
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, payload)
					}
				}))
				defer srv.Close()
				accounts := issue715AccountsOfType(service.AccountTypeAPIKey)
				for i := range accounts {
					accounts[i].Credentials["base_url"] = srv.URL
				}
				newNode := func(enabled bool) *OpenAIGatewayHandler {
					cfg := &config.Config{RunMode: config.RunModeSimple}
					cfg.Security.URLAllowlist.AllowInsecureHTTP = true
					settings := service.NewSettingService(&omit400SettingsRepo{enabled400: enabled, enabled409: scenario != "standalone"}, cfg)
					h := newOpenAIResponsesFailoverTestHandlerWithSettings(t, testutil.NewRealHTTPUpstream(cfg), repository.NewGatewayCache(clients()), settings, accounts...)
					h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
					return h
				}
				first := issue758Request(t, newNode(false), strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1))
				require.Equal(t, http.StatusOK, first.Code, first.Body.String())
				owners := issue805Owners(t, clients())
				require.Len(t, owners, 1)
				if scenario != "standalone" {
					accounts[0].Schedulable = false
				}
				answer := issue758Request(t, newNode(scenario != "400_off"), followup)
				mu.Lock()
				defer mu.Unlock()
				if scenario == "400_off" {
					require.Equal(t, http.StatusBadRequest, answer.Code, answer.Body.String())
					require.Len(t, bodies, 2, "one producer, one rejected continuation")
				} else {
					require.Len(t, bodies, 3, "one producer, one rejection, one lossy retry: %s", answer.Body.String())
					require.Equal(t, identities[1], identities[2], "400 recovery must stay on the same account")
					if scenario == "still_rejected" {
						require.Equal(t, http.StatusBadRequest, answer.Code, answer.Body.String())
					} else if scenario == "retry_503" || scenario == "retry_disconnect" {
						require.GreaterOrEqual(t, answer.Code, http.StatusBadRequest, answer.Body.String())
					} else {
						require.Equal(t, http.StatusOK, answer.Code, answer.Body.String())
						require.Contains(t, answer.Body.String(), "continued from readable history")
					}
				}
				require.Equal(t, owners, issue805Owners(t, clients()), "original ownership records must remain unchanged")
			})
		}
	}
}
