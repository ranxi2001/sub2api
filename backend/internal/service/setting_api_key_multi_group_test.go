//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type multiGroupSettingRepo struct {
	SettingRepository
	value string
	err   error
}

func (r *multiGroupSettingRepo) GetValue(context.Context, string) (string, error) {
	return r.value, r.err
}

func TestIssue1081PolicyDefaultAndReadFailure(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		err         error
		want, fail  bool
	}{
		{name: "legacy missing", err: ErrSettingNotFound, want: true},
		{name: "empty", want: true},
		{name: "enabled", value: "true", want: true},
		{name: "disabled", value: "false"},
		{name: "unavailable", err: errors.New("offline"), fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewSettingService(&multiGroupSettingRepo{value: tc.value, err: tc.err}, &config.Config{})
			got, err := svc.APIKeyMultiGroupEnabled(context.Background())
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.fail, err != nil)
		})
	}
}

func TestIssue1081PublicSettingsAndInjection(t *testing.T) {
	for _, value := range []string{"", "true", "false"} {
		svc := NewSettingService(&settingPublicRepoStub{values: map[string]string{
			SettingKeyAPIKeyMultiGroupEnabled: value,
			SettingKeySMTPPassword:            "private-fixture",
		}}, &config.Config{})
		public, err := svc.GetPublicSettings(context.Background())
		require.NoError(t, err)
		require.Equal(t, value != "false", public.APIKeyMultiGroupEnabled)
		payload, err := svc.GetPublicSettingsForInjection(context.Background())
		require.NoError(t, err)
		raw, err := json.Marshal(payload)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "private-fixture")
		var fields map[string]any
		require.NoError(t, json.Unmarshal(raw, &fields))
		require.Equal(t, value != "false", fields["api_key_multi_group_enabled"])
	}
}
