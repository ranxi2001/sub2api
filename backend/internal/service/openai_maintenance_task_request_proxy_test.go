package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type maintenanceTaskProxyRepo struct {
	ProxyRepository
	proxies map[int64]*Proxy
}

func (r *maintenanceTaskProxyRepo) GetByID(_ context.Context, id int64) (*Proxy, error) {
	return r.proxies[id], nil
}

type maintenanceTaskRotationRepo struct {
	*rotationTestRepo
	finished bool
	code     string
}

func (r *maintenanceTaskRotationRepo) FinishTOTPRotation(_ context.Context, _ int64, _ string, _ bool, code string) error {
	r.finished = true
	r.code = code
	return nil
}

type maintenanceTaskSessionRepo struct {
	OpenAISessionExportRepository
	record *OpenAISessionExportTaskRecord
}

func (r *maintenanceTaskSessionRepo) ExpireActiveSessionExportTasks(context.Context) error {
	return nil
}
func (r *maintenanceTaskSessionRepo) ClaimSessionExportTask(_ context.Context, worker string, _ time.Duration) (*OpenAISessionExportTaskRecord, error) {
	r.record.WorkerID = worker
	return r.record, nil
}
func (r *maintenanceTaskSessionRepo) FinishSessionExportTask(_ context.Context, _ int64, _ string, status, code string) error {
	r.record.Status = status
	r.record.ErrorCode = code
	return nil
}

func newMaintenanceTaskProxyService(t *testing.T) (*OpenAIOAuthReauthService, *reauthTestAccountReader, *maintenanceTaskRotationRepo, *maintenanceTaskSessionRepo, *maintenanceTaskProxyRepo) {
	t.Helper()
	svc, base := newRotationService(t)
	t.Cleanup(svc.oauth.Stop)
	reader, ok := svc.accounts.(*reauthTestAccountReader)
	require.True(t, ok)
	legacyID := int64(90)
	reader.account.ProxyID = &legacyID
	reader.account.Extra = map[string]any{
		"openai_proxy_mode": "request",
		"openai_proxy_ids":  []any{float64(1), float64(2)},
	}
	reader.proxy = &Proxy{ID: 90, Status: StatusActive, Protocol: "http", Host: "outside.example.test", Port: 8080}
	proxies := &maintenanceTaskProxyRepo{proxies: map[int64]*Proxy{
		1: {ID: 1, Status: StatusActive, Protocol: "http", Host: "expired.example.test", Port: 8080, ExpiresAt: func() *time.Time { expired := time.Now().Add(-time.Hour); return &expired }()},
		2: {ID: 2, Status: StatusActive, Protocol: "http", Host: "selected.example.test", Port: 8080},
	}}
	svc.oauth.proxyRepo = proxies
	rotation := &maintenanceTaskRotationRepo{rotationTestRepo: base}
	svc.repo = rotation
	session := &maintenanceTaskSessionRepo{record: &OpenAISessionExportTaskRecord{
		OpenAISessionExportTask: OpenAISessionExportTask{ID: 8, AccountID: 42, Status: OpenAISessionExportStatusRunning},
	}}
	svc.SetSessionExportRepository(session)
	return svc, reader, rotation, session, proxies
}

func TestOpenAIMaintenanceTaskRequestProxyUsesSelectedGroup(t *testing.T) {
	for _, operation := range []string{"totp", "session"} {
		t.Run(operation, func(t *testing.T) {
			svc, reader, _, _, _ := newMaintenanceTaskProxyService(t)
			if operation == "totp" {
				claim, err := svc.ClaimTOTPRotation(t.Context(), "worker")
				require.NoError(t, err)
				require.NotNil(t, claim)
				require.Equal(t, "http://selected.example.test:8080", claim.ProxyURL)
			} else {
				claim, err := svc.ClaimSessionExportTask(t.Context(), "worker", []string{OpenAISessionExportOperation})
				require.NoError(t, err)
				require.NotNil(t, claim)
				require.Equal(t, "http://selected.example.test:8080", claim.ProxyURL)
			}
			require.EqualValues(t, 90, *reader.account.ProxyID)
		})
	}
}

func TestOpenAIMaintenanceTaskRequestProxyRejectsUnavailableOrOutsideSources(t *testing.T) {
	for _, operation := range []string{"totp", "session"} {
		for _, condition := range []string{"unavailable", "unavailable-direct", "fixed_connection", "managed_proxy", "mihomo"} {
			t.Run(operation+"/"+condition, func(t *testing.T) {
				svc, reader, rotation, session, proxies := newMaintenanceTaskProxyService(t)
				switch condition {
				case "unavailable", "unavailable-direct":
					proxies.proxies = nil
					if condition == "unavailable-direct" {
						reader.account.ProxyID = nil
					}
				case "fixed_connection":
					reader.account.AuthConnectionID = "outside-fixed-connection"
				default:
					rotation.config.ProxySource = condition
					if condition == "managed_proxy" {
						id := int64(90)
						rotation.config.ProxyID = &id
					}
				}
				mihomoCalls := 0
				svc.acquireMihomoProxy = func(context.Context, string) (string, func(), error) {
					mihomoCalls++
					return "http://outside-mihomo.example.test:8080", func() {}, nil
				}
				if operation == "totp" {
					claim, err := svc.ClaimTOTPRotation(t.Context(), "worker")
					require.Error(t, err)
					require.Nil(t, claim)
					require.True(t, rotation.finished)
					require.Equal(t, "configuration_unavailable", rotation.code)
				} else {
					claim, err := svc.ClaimSessionExportTask(t.Context(), "worker", []string{OpenAISessionExportOperation})
					require.NoError(t, err)
					require.Nil(t, claim)
					require.Equal(t, OpenAISessionExportStatusFailed, session.record.Status)
					require.Equal(t, "proxy_unavailable", session.record.ErrorCode)
				}
				require.Zero(t, mihomoCalls)
			})
		}
	}
}

func TestOpenAIMaintenanceTaskRequestProxyPreservesExplicitSingle(t *testing.T) {
	for _, operation := range []string{"totp", "session"} {
		t.Run(operation, func(t *testing.T) {
			svc, reader, _, _, _ := newMaintenanceTaskProxyService(t)
			reader.account.Extra["openai_proxy_mode"] = "single"
			reader.account.Extra["openai_excel_bps"] = true
			reader.account.Extra["openai_excel_bps_proxy_mode"] = "request"
			reader.account.Extra["openai_excel_bps_proxy_ids"] = []any{float64(2)}
			if operation == "totp" {
				claim, err := svc.ClaimTOTPRotation(t.Context(), "worker")
				require.NoError(t, err)
				require.NotNil(t, claim)
				require.Equal(t, "http://outside.example.test:8080", claim.ProxyURL)
			} else {
				claim, err := svc.ClaimSessionExportTask(t.Context(), "worker", []string{OpenAISessionExportOperation})
				require.NoError(t, err)
				require.NotNil(t, claim)
				require.Equal(t, "http://outside.example.test:8080", claim.ProxyURL)
			}
		})
	}
}
