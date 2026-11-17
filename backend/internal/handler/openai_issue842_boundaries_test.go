//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type issue842FaultCache struct {
	service.GatewayCache
	issue715WSDeliveryCache
	fault  string
	reads  atomic.Int64
	client *redis.Client
}

// Exercise actual account builders after the handler has frozen the complete
// input. These rewrites have semantics and are outside the metadata allowlist.
func TestIssue842HTTPBuilderChangesStopBeforeNetwork(t *testing.T) {
	for _, kind := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		for _, change := range []string{"namespace", "timezone", "system_promotion"} {
			if kind == service.AccountTypeAPIKey && change == "system_promotion" {
				continue
			}
			t.Run(kind+"/"+change, func(t *testing.T) {
				clients := issue805Redis(t)
				u := &issue805Oracle{}
				srv := u.server(t)
				target, err := url.Parse(srv.URL)
				require.NoError(t, err)
				accounts := issue715Accounts()
				for i := range accounts {
					accounts[i].Type = kind
					accounts[i].Credentials = map[string]any{"api_key": "fixture842", "access_token": "fixture842", "base_url": srv.URL}
				}
				body, want := issue842History(t, false, "reasoning_status")
				settingsRepo := &issue842Settings{values: map[string]string{}}
				switch change {
				case "namespace":
					body, err = sjson.Set(body, "input.1.namespace", "semantic-namespace842")
				case "system_promotion":
					body, err = sjson.Set(body, "input.1.role", "system")
				case "timezone":
					body, err = sjson.Set(body, "input.1.content", "<environment_context>\n<timezone>UTC</timezone>\n</environment_context>")
					settingsRepo.values["openai_request_timezone_enabled"] = "true"
					settingsRepo.values["openai_request_timezone_auto"] = "false"
				}
				require.NoError(t, err)
				cfg := &config.Config{RunMode: config.RunModeSimple}
				cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				upstream := &issue842Transport{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), target: target}
				h := newOpenAIResponsesFailoverTestHandlerWithSettings(t, upstream, repository.NewGatewayCache(clients()), service.NewSettingService(settingsRepo, cfg), accounts...)
				h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
				issue805Policy(t, h.cfg, true)
				answer := issue758Request(t, h, body)
				if change == "timezone" {
					require.Equal(t, http.StatusOK, answer.Code, "Issue892 permits the administrator's finite timezone projection: %s", answer.Body.String())
					want, setErr := sjson.Set(want, "input.1.content", "<environment_context>\n<timezone>"+service.DefaultOpenAIRequestTimezone+"</timezone>\n</environment_context>")
					require.NoError(t, setErr)
					u.mu.Lock()
					defer u.mu.Unlock()
					require.Len(t, u.frames, 1)
					require.JSONEq(t, gjson.Get(want, "input").Raw, gjson.GetBytes(u.frames[0], "input").Raw)
					require.Equal(t, "9007199254740993", gjson.GetBytes(u.frames[0], "input.1.sequence").Raw)
					return
				}
				require.Equal(t, http.StatusConflict, answer.Code, answer.Body.String())
				require.Contains(t, answer.Body.String(), "encrypted_agent_recovery_wire_changed")
				u.mu.Lock()
				defer u.mu.Unlock()
				require.Empty(t, u.frames, "a gateway builder cannot rebase trust onto its changed input")
				require.Empty(t, issue805Owners(t, clients()))
			})
		}
	}
}

func (c *issue842FaultCache) GetOpenAIAgentRecovery(ctx context.Context, key string) (string, error) {
	reads := c.reads.Add(1)
	if c.fault == "redis_error" {
		_ = c.client.Close()
		return c.issue715WSDeliveryCache.GetOpenAIAgentRecovery(ctx, key)
	}
	if c.fault == "lost_after_snapshot" && reads > 1 {
		if err := c.client.Del(ctx, "openai_agent_owner:v1:"+key).Err(); err != nil {
			return "", err
		}
	}
	return c.issue715WSDeliveryCache.GetOpenAIAgentRecovery(ctx, key)
}

func TestIssue842HTTPPolicyAndDeliveryBoundaries(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, state := range []string{"complete", "pending", "failed", "conflict", "redis_error", "lost_after_snapshot", "canceled", "pointer"} {
			for _, disabled := range []bool{false, true} {
				for _, beta := range []bool{false, true} {
					t.Run(fmt.Sprintf("stream=%t/%s/optout=%t/beta=%t", stream, state, disabled, beta), func(t *testing.T) {
						clients := issue805Redis(t)
						u := &issue805Oracle{}
						srv := u.server(t)
						accounts := issue715AccountsOfType(service.AccountTypeAPIKey)
						for i := range accounts {
							accounts[i].Credentials = map[string]any{"api_key": []string{"fixture-ws-one", "fixture-ws-two"}[i], "base_url": srv.URL}
						}
						newNode := func(relaxed, lossy bool, fault string) *OpenAIGatewayHandler {
							cfg := &config.Config{RunMode: config.RunModeSimple}
							cfg.Security.URLAllowlist.AllowInsecureHTTP = true
							client := clients()
							cache := repository.NewGatewayCache(client)
							if fault != "" {
								cache = &issue842FaultCache{GatewayCache: cache, issue715WSDeliveryCache: cache.(issue715WSDeliveryCache), fault: fault, client: client}
							}
							settings := service.NewSettingService(&issue842Settings{beta: lossy}, cfg)
							h := newOpenAIResponsesFailoverTestHandlerWithSettings(t, testutil.NewRealHTTPUpstream(cfg), cache, settings, accounts...)
							h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
							issue805Policy(t, h.cfg, relaxed)
							return h
						}
						first := issue758Request(t, newNode(false, false, ""), strings.Replace(issue715Body(false, stream), issue715Agent+",", "", 1))
						require.Equal(t, http.StatusOK, first.Code, first.Body.String())
						original := issue805Owners(t, clients())
						require.Len(t, original, 1)
						probe := clients()
						if state == "pending" || state == "failed" || state == "conflict" {
							for key, raw := range original {
								value := state
								if state == "pending" {
									value = "pending:fixture842:" + raw
								}
								require.NoError(t, probe.Set(context.Background(), key, value, time.Minute).Err())
							}
						}
						before := issue805Owners(t, clients())
						accounts[0].Schedulable = false
						body, want := issue842History(t, stream, "reasoning_status")
						if state == "pointer" {
							body = strings.TrimSuffix(body, "}") + fmt.Sprintf(`,"previous_response_id":%q}`, issue758CompletedResponse(t, first.Body.String(), stream).Get("id").String())
						}
						fault := ""
						if state == "redis_error" || state == "lost_after_snapshot" {
							fault = state
						}
						h := newNode(disabled, beta, fault)
						var code int
						var response string
						if state == "canceled" {
							code, response = issue715Request(t, h, body, 715, "fixture-758-thread", true)
						} else {
							answer := issue758Request(t, h, body)
							code, response = answer.Code, answer.Body.String()
						}
						allowed := state == "complete" && (disabled || beta)
						// Beta explicitly detaches an old provider pointer before any
						// send; ordinary opt-out cannot grant this lossy behavior.
						allowed = allowed || (state == "pointer" && beta)
						u.mu.Lock()
						defer u.mu.Unlock()
						if allowed {
							require.Equal(t, http.StatusOK, code, response)
							require.Len(t, u.frames, 2)
							if disabled && state == "complete" {
								require.JSONEq(t, gjson.Get(want, "input").Raw, gjson.GetBytes(u.frames[1], "input").Raw)
							}
						} else {
							require.Len(t, u.frames, 1, "unsafe history must stop before B, regardless of metadata or Beta: %s", response)
							if state != "canceled" {
								require.GreaterOrEqual(t, code, http.StatusBadRequest, response)
							}
						}
						for key, raw := range before {
							if state == "lost_after_snapshot" {
								current := issue805Owners(t, clients())[key]
								require.True(t, current == "" || current == raw, "lost records cannot be republished")
								continue
							}
							require.Equal(t, raw, issue805Owners(t, clients())[key])
						}
					})
				}
			}
		}
	}
}
