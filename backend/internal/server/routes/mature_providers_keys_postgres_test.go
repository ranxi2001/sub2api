//go:build integration

package routes

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMatureProvidersKeyCreationIdempotency(t *testing.T) {
	for _, scenario := range []string{"confirmed", "upstream_uncertain", "persistence_failure"} {
		t.Run(scenario, func(t *testing.T) {
			uncertain := scenario == "upstream_uncertain"
			db := matureProviderDatabase(t)
			cfg := &config.Config{}
			cfg.Totp.EncryptionKey = strings.Repeat("34", 32)
			crypt, err := repository.NewAESEncryptor(cfg)
			require.NoError(t, err)
			var creates atomic.Int32
			client := &http.Client{Transport: matureProviderTransport(func(r *http.Request) (*http.Response, error) {
				status := 200
				raw := `{"code":0,"data":{}}`
				switch r.URL.Path {
				case "/api/v1/groups/rates":
					raw = `{"code":0,"data":{"4":0.25}}`
				case "/api/v1/groups/available":
					raw = `{"code":0,"data":[{"id":4,"name":"Synthetic group","platform":"openai","rate_multiplier":1}]}`
				case "/api/v1/keys":
					if r.Method == "POST" {
						creates.Add(1)
						require.NotEmpty(t, r.Header.Get("Idempotency-Key"))
						raw = `{"code":0,"data":{"id":5,"name":"Synthetic created key"}}`
						if uncertain {
							status = 503
							raw = `{"message":"synthetic uncertain failure"}`
						}
					} else {
						raw = `{"code":0,"data":{"items":[],"total":0}}`
					}
				default:
					t.Errorf("unexpected remote request %s %s", r.Method, r.URL.Path)
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw))}, nil
			})}
			newRouter := func() *gin.Engine {
				ch := admin.NewChannelHandler(service.NewChannelService(repository.NewChannelRepository(db), nil, nil, nil, nil), nil, nil)
				if dep, ok := any(ch).(interface {
					SetMatureProviderDependencies(service.SecretEncryptor, *http.Client)
				}); ok {
					dep.SetMatureProviderDependencies(crypt, client)
				}
				r := gin.New()
				registerChannelRoutes(r.Group("/api/v1/admin"), &handler.Handlers{Admin: &handler.AdminHandlers{Channel: ch}})
				return r
			}
			a, b := newRouter(), newRouter()
			request := func(r *gin.Engine, path, body, key string) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/api/v1/admin/providers"+path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", key)
				r.ServeHTTP(w, req)
				return w
			}
			created := request(a, "", `{"name":"Fixture","site_type":"sub2api","base_url":"https://provider.example.invalid","auth_mode":"token","access_token":"synthetic-token"}`, "")
			require.Equal(t, 200, created.Code, created.Body.String())
			var envelope struct{ Data struct{ ID, Revision int64 } }
			require.NoError(t, json.Unmarshal(created.Body.Bytes(), &envelope))
			path := fmt.Sprintf("/%d/keys", envelope.Data.ID)
			body := fmt.Sprintf(`{"group_id":"4","revision":%d,"confirm":true}`, envelope.Data.Revision)
			if scenario == "persistence_failure" {
				_, err = db.Exec(`CREATE FUNCTION reject_key_result() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'synthetic result persistence failure'; END$$;
                CREATE TRIGGER reject_key_result BEFORE UPDATE ON mature_provider_key_operations FOR EACH ROW EXECUTE FUNCTION reject_key_result()`)
				require.NoError(t, err)
			}
			first := request(a, path, body, "synthetic-key-create-0001")
			if scenario == "persistence_failure" {
				require.Equal(t, 502, first.Code, "local persistence failure after upstream creation must be classified as uncertain")
				require.Contains(t, first.Body.String(), "PROVIDER_KEY_CREATE_UNCERTAIN")
				second := request(b, path, body, "synthetic-key-create-0001")
				require.Equal(t, 409, second.Code)
				require.Contains(t, second.Body.String(), "PROVIDER_KEY_CREATE_PENDING")
				require.EqualValues(t, 1, creates.Load(), "a pending tombstone must prevent duplicate remote writes")
				return
			}

			want := 200
			if uncertain {
				want = 502
			}
			require.Equal(t, want, first.Code, first.Body.String())
			second := request(b, path, body, "synthetic-key-create-0001")
			require.Equal(t, want, second.Code, second.Body.String())
			require.JSONEq(t, first.Body.String(), second.Body.String(), "second instance must replay the durable result")
			require.EqualValues(t, 1, creates.Load(), "ambiguous writes must never be automatically repeated")
			require.Equal(t, 409, request(b, path, strings.Replace(body, `"4"`, `"9"`, 1), "synthetic-key-create-0001").Code, "an idempotency key cannot be reused for another group")
		})
	}
}
