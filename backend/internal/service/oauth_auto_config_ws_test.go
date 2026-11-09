package service

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOAuthAutoConfigWSDefaultsLegacyAndRoundTrip(t *testing.T) {
	defaults := DefaultOAuthAutoConfig()
	require.NotNil(t, defaults.OpenAIWSMode)
	require.Equal(t, OpenAIWSIngressModeCtxPool, *defaults.OpenAIWSMode)
	require.NotNil(t, defaults.OpenAIWSSSEAcceleration)
	require.False(t, *defaults.OpenAIWSSSEAcceleration)

	repo := &accountOpsSettingsStub{raw: `{"enabled":false,"platform":"openai"}`}
	cfg, err := GetOAuthAutoConfig(t.Context(), repo)
	require.NoError(t, err)
	require.Nil(t, cfg.OpenAIWSMode, "legacy templates must not take ownership of imported WS settings")
	require.Nil(t, cfg.OpenAIWSSSEAcceleration)
	svc := NewAccountOpsService(repo, nil, nil)
	svc.autoGroups = autoConfigGroups{}
	for _, mode := range []string{OpenAIWSIngressModeOff, OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough, OpenAIWSIngressModeHTTPBridge} {
		for _, accelerate := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/SSE-off", true: "/SSE-on"}[accelerate], func(t *testing.T) {
				cfg.OpenAIWSMode, cfg.OpenAIWSSSEAcceleration = &mode, &accelerate
				_, err := svc.SaveOAuthAutoConfig(t.Context(), cfg)
				require.NoError(t, err)
				loaded, err := GetOAuthAutoConfig(t.Context(), repo)
				require.NoError(t, err)
				require.Equal(t, cfg.OpenAIWSMode, loaded.OpenAIWSMode)
				require.Equal(t, cfg.OpenAIWSSSEAcceleration, loaded.OpenAIWSSSEAcceleration)
			})
		}
	}
}

func TestOAuthAutoConfigWSValidation(t *testing.T) {
	for _, mode := range []string{"", "enabled", "CTX_POOL", " ctx_pool ", OpenAIWSIngressModeShared, OpenAIWSIngressModeDedicated} {
		t.Run(mode, func(t *testing.T) {
			cfg := DefaultOAuthAutoConfig()
			cfg.OpenAIWSMode = &mode
			require.Error(t, ValidateOAuthAutoConfig(cfg))
		})
	}
	for _, raw := range []string{`{"openai_ws_mode":true}`, `{"openai_ws_sse_acceleration":"true"}`} {
		_, err := GetOAuthAutoConfig(t.Context(), &accountOpsSettingsStub{raw: raw})
		require.Error(t, err)
	}
}

func TestOAuthAutoConfigWSOverridesImports(t *testing.T) {
	for _, mode := range []string{OpenAIWSIngressModeOff, OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough, OpenAIWSIngressModeHTTPBridge} {
		for _, accelerate := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/SSE-off", true: "/SSE-on"}[accelerate], func(t *testing.T) {
				cfg := DefaultOAuthAutoConfig()
				cfg.Enabled, cfg.GroupIDs = true, []int64{2}
				cfg.OpenAIWSMode, cfg.OpenAIWSSSEAcceleration = &mode, &accelerate
				raw, err := json.Marshal(cfg)
				require.NoError(t, err)
				svc := &adminServiceImpl{
					settingService: NewSettingService(&accountOpsSettingsStub{raw: string(raw)}, nil),
					groupRepo:      autoConfigGroups{group: &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}},
				}
				original := map[string]any{
					"openai_oauth_responses_websockets_v2_mode":    OpenAIWSIngressModePassthrough,
					"openai_oauth_responses_websockets_v2_enabled": mode == OpenAIWSIngressModeOff,
					"openai_oauth_ws_sse_acceleration":             !accelerate,
					"responses_websockets_v2_enabled":              true,
					"openai_ws_enabled":                            true,
					"openai_passthrough":                           false,
					"keep":                                         "unchanged",
				}
				snapshot := maps.Clone(original)
				input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: original}
				require.NoError(t, svc.ApplyOAuthAutoConfig(t.Context(), input))
				require.Equal(t, snapshot, original, "the caller's imported map must remain unchanged")
				require.Equal(t, "unchanged", input.Extra["keep"])
				require.Equal(t, false, input.Extra["openai_passthrough"], "initialization must not change other transport gates")
				require.Equal(t, mode, input.Extra["openai_oauth_responses_websockets_v2_mode"])
				account := &Account{Platform: input.Platform, Type: input.Type, Extra: input.Extra}
				require.Equal(t, mode, account.ResolveOpenAIResponsesWebSocketV2Mode(OpenAIWSIngressModeCtxPool))
				require.Equal(t, mode != OpenAIWSIngressModeOff, account.IsOpenAIResponsesWebSocketV2Enabled(), "typed toggle must override legacy WS opt-ins")
				require.Equal(t, accelerate, account.IsOpenAIOAuthWSSSEAccelerationEnabled())
			})
		}
	}
}

func TestOAuthAutoConfigWSMissingFieldsAreIndependent(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		wantMode     string
		wantEnabled  bool
		wantSSE      bool
	}{
		{"legacy", "", OpenAIWSIngressModePassthrough, true, true},
		{"mode only", `,"openai_ws_mode":"off"`, OpenAIWSIngressModeOff, false, true},
		{"acceleration only", `,"openai_ws_sse_acceleration":false`, OpenAIWSIngressModePassthrough, true, false},
		{"null leaves imports", `,"openai_ws_mode":null,"openai_ws_sse_acceleration":null`, OpenAIWSIngressModePassthrough, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &accountOpsSettingsStub{raw: `{"enabled":true,"platform":"openai","group_ids":[2]` + tc.fields + `}`}
			svc := &adminServiceImpl{
				settingService: NewSettingService(repo, nil),
				groupRepo:      autoConfigGroups{group: &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}},
			}
			input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
				"openai_oauth_responses_websockets_v2_mode":    OpenAIWSIngressModePassthrough,
				"openai_oauth_responses_websockets_v2_enabled": true,
				"openai_oauth_ws_sse_acceleration":             true,
			}}
			require.NoError(t, svc.ApplyOAuthAutoConfig(t.Context(), input))
			require.Equal(t, tc.wantMode, input.Extra["openai_oauth_responses_websockets_v2_mode"])
			require.Equal(t, tc.wantEnabled, input.Extra["openai_oauth_responses_websockets_v2_enabled"])
			require.Equal(t, tc.wantSSE, input.Extra["openai_oauth_ws_sse_acceleration"])
		})
	}
}

func TestOAuthAutoConfigWSScope(t *testing.T) {
	for _, tc := range []struct {
		name, platform, kind, templatePlatform string
		enabled, observer                      bool
	}{
		{name: "initialization disabled"},
		{name: "API key", enabled: true, kind: AccountTypeAPIKey},
		{name: "setup token", enabled: true, kind: AccountTypeSetupToken},
		{name: "another OAuth platform", enabled: true, platform: PlatformAnthropic},
		{name: "matching non-OpenAI template", enabled: true, platform: PlatformAnthropic, templatePlatform: PlatformAnthropic},
		{name: "observer", enabled: true, observer: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultOAuthAutoConfig()
			cfg.Enabled, cfg.GroupIDs = tc.enabled, []int64{2}
			if tc.templatePlatform != "" {
				cfg.Platform = tc.templatePlatform
			}
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			svc := &adminServiceImpl{
				settingService: NewSettingService(&accountOpsSettingsStub{raw: string(raw)}, nil),
				groupRepo:      autoConfigGroups{group: &Group{ID: 2, Platform: cfg.Platform, Status: StatusActive}},
			}
			input := &CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, GroupIDs: []int64{25}, Extra: map[string]any{"keep": true}}
			if tc.platform != "" {
				input.Platform = tc.platform
			}
			if tc.kind != "" {
				input.Type = tc.kind
			}
			ctx := t.Context()
			if tc.observer {
				ctx = WithObserverScope(ctx, []int64{25})
			}
			require.NoError(t, svc.ApplyOAuthAutoConfig(ctx, input))
			require.NotContains(t, input.Extra, "openai_oauth_responses_websockets_v2_mode")
			require.NotContains(t, input.Extra, "openai_oauth_responses_websockets_v2_enabled")
			require.NotContains(t, input.Extra, "openai_oauth_ws_sse_acceleration")
		})
	}
}
