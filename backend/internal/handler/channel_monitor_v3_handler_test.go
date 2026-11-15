package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// monitorV3HandlerRepo serves only what Status reads; other methods panic.
type monitorV3HandlerRepo struct {
	service.ChannelMonitorV3Repository
	components []service.ChannelMonitorV3Component
	facts      service.ChannelMonitorV3Facts
	idleProbe  service.ChannelMonitorV3IdleProbe
	probes     []service.ChannelMonitorV3ProbeFact
}

func (r *monitorV3HandlerRepo) GetConfig(context.Context) (*service.ChannelMonitorV3Config, error) {
	return &service.ChannelMonitorV3Config{Version: 1, IntervalMinutes: 5, Cells: 30, AvailabilityRange: "7d",
		DownErrorRate: 0.2, DegradedErrorRate: 0.05, DegradedTTFTMs: 10000, MinRequests: 1, IdleProbe: r.idleProbe}, nil
}
func (r *monitorV3HandlerRepo) UpdateIdleProbe(_ context.Context, probe service.ChannelMonitorV3IdleProbe) error {
	r.idleProbe = probe
	return nil
}
func (r *monitorV3HandlerRepo) ProbeFacts(_ context.Context, _ []int64, _, _ time.Time, interval time.Duration) ([]service.ChannelMonitorV3ProbeFact, error) {
	if interval == 0 {
		return nil, nil
	}
	return r.probes, nil
}
func (r *monitorV3HandlerRepo) ListCategories(context.Context) ([]service.ChannelMonitorV3Category, error) {
	return []service.ChannelMonitorV3Category{{ID: 1, Name: "GPT"}}, nil
}
func (r *monitorV3HandlerRepo) ListComponents(context.Context) ([]service.ChannelMonitorV3Component, error) {
	return r.components, nil
}
func (r *monitorV3HandlerRepo) SlotFacts(context.Context, []int64, time.Time, time.Time, time.Duration, bool) (*service.ChannelMonitorV3Facts, error) {
	return &r.facts, nil
}
func (r *monitorV3HandlerRepo) RangeFacts(context.Context, []int64, time.Time, time.Time) (*service.ChannelMonitorV3Facts, error) {
	return &service.ChannelMonitorV3Facts{}, nil
}
func (r *monitorV3HandlerRepo) DataThrough(context.Context) (*time.Time, error) { return nil, nil }

type monitorV3AccessStub struct {
	groups []service.Group
	rates  map[int64]float64
	err    error
}

func (s *monitorV3AccessStub) GetAvailableGroups(context.Context, int64) ([]service.Group, error) {
	return s.groups, s.err
}
func (s *monitorV3AccessStub) GetUserGroupRates(context.Context, int64) (map[int64]float64, error) {
	return s.rates, nil
}

type monitorModeSetterStub struct{ modes []string }

func (s *monitorModeSetterStub) SetChannelMonitorMode(_ context.Context, mode string) (string, error) {
	s.modes = append(s.modes, mode)
	if mode != "v3" {
		return "", service.ErrChannelMonitorInvalidMode
	}
	return mode, nil
}

func monitorV3HandlerFixture() (*ChannelMonitorV3Handler, *monitorV3AccessStub) {
	h, access, _ := monitorV3HandlerFixtureWithRepo()
	return h, access
}

func monitorV3HandlerFixtureWithRepo() (*ChannelMonitorV3Handler, *monitorV3AccessStub, *monitorV3HandlerRepo) {
	cat := int64(1)
	slot := time.Now().UTC().Truncate(5 * time.Minute)
	repo := &monitorV3HandlerRepo{
		components: []service.ChannelMonitorV3Component{
			{ID: 1, CategoryID: &cat, Name: "Mine", GroupID: 4, Enabled: true, Visibility: service.ChannelMonitorV3VisibilityGroup, ShowMultiplier: true, GroupRateMultiplier: 0.2},
			{ID: 2, CategoryID: &cat, Name: "Exclusive", GroupID: 9, Enabled: true, Visibility: service.ChannelMonitorV3VisibilityGroup},
		},
		facts: service.ChannelMonitorV3Facts{Metrics: []service.ChannelMonitorV3Fact{{GroupID: 4, Model: "gpt", Slot: slot, Success: 1, Errors: 4321}}},
	}
	access := &monitorV3AccessStub{groups: []service.Group{{ID: 4}}, rates: map[int64]float64{4: 0.15}}
	return &ChannelMonitorV3Handler{service: service.NewChannelMonitorV3Service(repo, nil), access: access}, access, repo
}

func monitorV3Request(target string, role string, body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
	if role != "" {
		c.Set(string(middleware.ContextKeyUserRole), role)
	}
	return c, recorder
}

func decodeMonitorV3Status(t *testing.T, recorder *httptest.ResponseRecorder) service.ChannelMonitorV3Status {
	t.Helper()
	var envelope struct {
		Code int                            `json:"code"`
		Data service.ChannelMonitorV3Status `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Zero(t, envelope.Code, recorder.Body.String())
	return envelope.Data
}

func TestChannelMonitorV3StatusScopesOrdinaryUsersServerSide(t *testing.T) {
	h, _ := monitorV3HandlerFixture()
	c, recorder := monitorV3Request("/channel-monitor-v3/status", service.RoleUser, "")
	h.Status(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	status := decodeMonitorV3Status(t, recorder)
	require.Len(t, status.Categories, 1)
	require.Len(t, status.Categories[0].Components, 1, "a group the user cannot use stays hidden")
	mine := status.Categories[0].Components[0]
	require.InDelta(t, 0.15, *mine.Multiplier, 1e-9)
	last := mine.Cells[len(mine.Cells)-1]
	require.NotNil(t, last)
	require.Equal(t, service.ChannelMonitorV3StatusDown, last.Status)
	require.Zero(t, last.Requests)
	// Inspect field names, not decimal substrings that can occur in timestamps
	// or public success rates. Check the wire JSON so even zero-valued private
	// fields cannot silently become part of the ordinary-user response.
	var wire map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &wire))
	var checkPrivateFields func(any)
	checkPrivateFields = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for _, field := range []string{"requests", "errors", "ignored_errors", "probes", "successes"} {
				require.NotContains(t, value, field, "request volume never reaches users")
			}
			for _, child := range value {
				checkPrivateFields(child)
			}
		case []any:
			for _, child := range value {
				checkPrivateFields(child)
			}
		}
	}
	checkPrivateFields(wire)
	// The exact timestamp that caused the CI failure is legitimate public data.
	data, ok := wire["data"].(map[string]any)
	require.True(t, ok, "status envelope must contain an object")
	data["generated_at"] = "2026-11-14T19:24:11.151432152Z"
	checkPrivateFields(wire)
}

func TestChannelMonitorV3StatusShowsAdminsEverything(t *testing.T) {
	h, access := monitorV3HandlerFixture()
	access.err = errors.New("must not be consulted for admins")
	c, recorder := monitorV3Request("/channel-monitor-v3/status", service.RoleAdmin, "")
	h.Status(c)
	status := decodeMonitorV3Status(t, recorder)
	require.Len(t, status.Categories[0].Components, 2)
	require.Contains(t, recorder.Body.String(), `"errors":4321`)
}

func TestChannelMonitorV3StatusRejectsBadInput(t *testing.T) {
	h, access := monitorV3HandlerFixture()
	c, recorder := monitorV3Request("/channel-monitor-v3/status?end=yesterday", service.RoleUser, "")
	h.Status(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)

	access.err = errors.New("lookup failed")
	c, recorder = monitorV3Request("/channel-monitor-v3/status", service.RoleUser, "")
	h.Status(c)
	require.NotEqual(t, http.StatusOK, recorder.Code, "no scope means no data")

	c, recorder = monitorV3Request("/channel-monitor-v3/status", "", "")
	c.Keys = nil
	h.Status(c)
	require.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestChannelMonitorV3SetModeOnlyTouchesTheMode(t *testing.T) {
	setter := &monitorModeSetterStub{}
	h := &ChannelMonitorV3Handler{settings: setter}
	c, recorder := monitorV3Request("/admin/channel-monitor-mode", service.RoleAdmin, `{"mode":"v3"}`)
	h.SetMode(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"code":0,"message":"success","data":{"mode":"v3"}}`, recorder.Body.String())

	c, recorder = monitorV3Request("/admin/channel-monitor-mode", service.RoleAdmin, `{"mode":"v9"}`)
	h.SetMode(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, []string{"v3", "v9"}, setter.modes)
}

func TestChannelMonitorV3UpdateIdleProbeSavesOnlyTheProbeSettings(t *testing.T) {
	h, _, repo := monitorV3HandlerFixtureWithRepo()
	c, recorder := monitorV3Request("/admin/channel-monitor-v3/idle-probe", service.RoleAdmin,
		`{"enabled":true,"groups":[{"group_id":4,"model":" gpt-5.5 ","reasoning_effort":"LOW"}]}`)
	h.UpdateIdleProbe(c)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.JSONEq(t, `{"code":0,"message":"success","data":{"enabled":true,"groups":[{"group_id":4,"model":"gpt-5.5","reasoning_effort":"low"}]}}`, recorder.Body.String())
	require.Equal(t, "gpt-5.5", repo.idleProbe.Groups[0].Model)

	c, recorder = monitorV3Request("/admin/channel-monitor-v3/idle-probe", service.RoleAdmin, `{"enabled":true,"groups":[{"group_id":4}]}`)
	h.UpdateIdleProbe(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code, "a group shown as a whole needs a model to ask")

	c, recorder = monitorV3Request("/admin/channel-monitor-v3/idle-probe", service.RoleAdmin, `{"enabled":"yes"}`)
	h.UpdateIdleProbe(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, "gpt-5.5", repo.idleProbe.Groups[0].Model, "rejected input changes nothing")
}

func TestChannelMonitorV3StatusMarksProbedSlotsWithoutVolumesForUsers(t *testing.T) {
	h, _, repo := monitorV3HandlerFixtureWithRepo()
	repo.facts = service.ChannelMonitorV3Facts{}
	repo.idleProbe = service.ChannelMonitorV3IdleProbe{Enabled: true, Groups: []service.ChannelMonitorV3IdleProbeGroup{{GroupID: 4, Model: "gpt"}}}
	slot := time.Now().UTC().Truncate(5 * time.Minute).Add(-5 * time.Minute)
	repo.probes = []service.ChannelMonitorV3ProbeFact{{GroupID: 4, Model: "gpt", Slot: slot, Count: 1, LatencyP50Ms: 1800}}

	c, recorder := monitorV3Request("/channel-monitor-v3/status", service.RoleUser, "")
	h.Status(c)
	status := decodeMonitorV3Status(t, recorder)
	require.True(t, status.IdleProbe)
	cell := status.Categories[0].Components[0].Cells[28]
	require.True(t, cell.Probed)
	require.Equal(t, int64(1800), *cell.ProbeLatencyMs)
	require.NotContains(t, recorder.Body.String(), `"probes"`)

	c, recorder = monitorV3Request("/channel-monitor-v3/status", service.RoleAdmin, "")
	h.Status(c)
	require.Contains(t, recorder.Body.String(), `"probes":1`)
}
