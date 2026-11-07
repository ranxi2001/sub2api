//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Exercise actual handlers through the real administrator creation service.
// Only storage and list/identity lookups are in-memory; no upstream is called.
type initialSelectionGroups struct{ service.AdminGroupRepository }

func (initialSelectionGroups) GetByID(context.Context, int64) (*service.Group, error) {
	return &service.Group{ID: 2, Platform: service.PlatformOpenAI, Status: service.StatusActive}, nil
}

type initialSelectionAccounts struct {
	service.AdminAccountRepository
	created []*service.Account
}

func (r *initialSelectionAccounts) Create(_ context.Context, a *service.Account) error {
	a.ID = int64(len(r.created) + 1)
	r.created = append(r.created, a)
	return nil
}
func (r *initialSelectionAccounts) BindGroups(context.Context, int64, []int64) error { return nil }

type initialSelectionAdmin struct {
	*codexImportMemoryAdminService
	creator service.AdminService
}

func (s *initialSelectionAdmin) CreateAccount(ctx context.Context, input *service.CreateAccountInput) (*service.Account, error) {
	account, err := s.creator.CreateAccount(ctx, input)
	if err == nil {
		s.accounts = append(s.accounts, *account)
	}
	return account, err
}
func initialSelectionHandler(t *testing.T) (*AccountHandler, *initialSelectionAccounts, *initialSelectionAdmin) {
	t.Helper()
	cfg := service.DefaultOAuthAutoConfig()
	cfg.Enabled = true
	cfg.GroupIDs = []int64{2}
	cfg.SelectedModels = []string{"gpt-6.1-sol"}
	cfg.ModelMappings = nil
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	settings := service.NewSettingService(&opsHandlerWritableSettings{raw: string(raw)}, nil)
	repo := &initialSelectionAccounts{}
	creator := service.NewAdminService(nil, nil, initialSelectionGroups{}, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, settings, nil, nil, nil, nil, nil, nil, nil, nil)
	admin := &initialSelectionAdmin{codexImportMemoryAdminService: newCodexImportMemoryAdminService(nil), creator: creator}
	handler := NewAccountHandler(admin, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	return handler, repo, admin
}
func TestOAuthInitialSelectionCreateAndImportHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := `{"name":"fixture","platform":"openai","type":"oauth","credentials":{"access_token":"fixture"},"concurrency":3,"priority":50}`
	for _, tc := range []struct {
		name, body string
		mount      func(*gin.Engine, *AccountHandler)
	}{
		{"ordinary create", account, func(r *gin.Engine, h *AccountHandler) { r.POST("/create", h.Create) }},
		{"batch import", `{"accounts":[` + account + `]}`, func(r *gin.Engine, h *AccountHandler) { r.POST("/create", h.BatchCreate) }},
		{"data import", `{"data":{"type":"` + dataType + `","version":` + "1" + `,"proxies":[],"accounts":[` + account + `]},"skip_default_group_bind":true}`, func(r *gin.Engine, h *AccountHandler) { r.POST("/create", h.ImportData) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, repo, _ := initialSelectionHandler(t)
			router := gin.New()
			tc.mount(router, h)
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/create", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(response, request)
			require.Equal(t, 200, response.Code, response.Body.String())
			require.Len(t, repo.created, 1, response.Body.String())
			require.True(t, repo.created[0].IsModelSupported("gpt-6.1-sol"))
			require.False(t, repo.created[0].IsModelSupported("gpt-6-luna"))
		})
	}
	t.Run("Codex new and existing", func(t *testing.T) {
		h, repo, admin := initialSelectionHandler(t)
		req := CodexSessionImportRequest{SkipDefaultGroupBind: boolPtr(true)}
		entries := []codexImportEntry{{Index: 1, Value: buildCodexAccessOnlyImportValue(t, "fixture-workspace", "fixture-user")}}
		first, err := h.importCodexSessions(t.Context(), req, entries)
		require.NoError(t, err)
		require.Equal(t, 1, first.Created)
		require.Len(t, repo.created, 1)
		require.False(t, repo.created[0].IsModelSupported("gpt-6-luna"))
		second, err := h.importCodexSessions(t.Context(), req, entries)
		require.NoError(t, err)
		require.Equal(t, 1, second.Updated)
		require.Len(t, repo.created, 1, "existing import must not recreate or reapply initialization")
		require.Len(t, admin.updatedAccounts, 1)
		require.Equal(t, []string{"gpt-6.1-sol"}, admin.updatedAccounts[0].input.Credentials[service.OAuthInitialModelSelectionKey])
	})
}
