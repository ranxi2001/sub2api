//go:build unit

package accountconnectiontiming

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamstats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Real account handlers/platform services send through the repository HTTP
// transport. No mocked response DTO supplies the error evidence.
func TestAccountUpstreamErrorsRealPlatformChain(t *testing.T) {
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformAnthropic} {
		t.Run(platform, func(t *testing.T) {
			id := int64(89101)
			if platform == service.PlatformAnthropic {
				id++
			}
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if platform == service.PlatformAnthropic {
					require.Equal(t, "/v1/messages", r.URL.Path)
				} else {
					require.Equal(t, "/v1/responses", r.URL.Path)
				}
				if calls == 1 {
					w.WriteHeader(503)
					_, _ = io.WriteString(w, "synthetic unavailable")
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if platform == service.PlatformAnthropic {
					_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"synthetic-model\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"OK\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				} else {
					_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_fixture\",\"object\":\"response\",\"created_at\":0,\"status\":\"in_progress\",\"model\":\"synthetic-model\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"item_id\":\"msg_fixture\",\"output_index\":0,\"content_index\":0,\"delta\":\"OK\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"resp_fixture\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":\"synthetic-model\",\"output\":[{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
				}
			}))
			defer upstream.Close()
			target, err := url.Parse(upstream.URL)
			require.NoError(t, err)
			account := &service.Account{ID: id, Platform: platform, Type: service.AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{"api_key": "synthetic-only", "base_url": "https://fixture.invalid"}, Extra: map[string]any{"responses_supported": true}}
			downstream := httptest.NewServer(timingRouter(account, target))
			defer downstream.Close()
			since := time.Now()
			for range 2 {
				response, err := http.Post(downstream.URL+"/api/v1/admin/accounts/"+strconv.FormatInt(id, 10)+"/test", "application/json", strings.NewReader("{\"model_id\":\"synthetic-model\"}"))
				require.NoError(t, err)
				body, readErr := io.ReadAll(response.Body)
				err = readErr
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				if calls == 2 {
					require.Contains(t, string(body), "\"success\":true", "legal successful stream must complete the real parser")
				}
			}
			require.Equal(t, 2, calls)
			evidence := upstreamstats.Default.Snapshot([]int64{id}, since, time.Now())[id]
			require.Equal(t, int64(2), evidence.Total)
			require.Equal(t, int64(1), evidence.Failures)
			require.NotNil(t, evidence.Percent)
			require.Equal(t, 50.0, *evidence.Percent)
		})
	}
}
