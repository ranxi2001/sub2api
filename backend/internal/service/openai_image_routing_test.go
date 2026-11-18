//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

const imageRoutingTextModel = "gpt-6-astra"

func imageRoutingAccount(id int64, models map[string]any) Account {
	return Account{
		ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"model_mapping": models},
	}
}

func TestOpenAIImageRoutingModelAllowlist(t *testing.T) {
	textOnly := imageRoutingAccount(1, map[string]any{imageRoutingTextModel: imageRoutingTextModel})
	imageCapable := imageRoutingAccount(2, map[string]any{imageRoutingTextModel: imageRoutingTextModel, "gpt-image-*": "gpt-image-*"})
	for _, tc := range []struct {
		name, payload, model string
		wantImage            bool
	}{
		{"native tool default", `{"tools":[{"type":"image_generation"}]}`, "gpt-image-2", true},
		{"native tool explicit", `{"tools":[{"type":"image_generation","model":"gpt-image-1"}]}`, "gpt-image-1", true},
		{"explicit tool choice", `{"tool_choice":{"type":"namespace","name":"image_gen"}}`, "gpt-image-2", true},
		{"passive namespace", `{"tools":[{"type":"namespace","name":"image_gen","tools":[]}]}`, "", false},
		{"plain text", `{"input":"hello"}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithOpenAIImageGenerationRequest(context.Background(), imageRoutingTextModel, []byte(tc.payload))
			require.Equal(t, tc.wantImage, OpenAIImageGenerationIntentFromContext(ctx))
			require.Equal(t, tc.model, openAIImageRequestModel(ctx, imageRoutingTextModel))
			if tc.wantImage {
				require.Equal(t, "image_model_not_supported", openAIImageRequestRejection(ctx, &textOnly, imageRoutingTextModel))
			} else {
				require.Empty(t, openAIImageRequestRejection(ctx, &textOnly, imageRoutingTextModel))
			}
			require.Empty(t, openAIImageRequestRejection(ctx, &imageCapable, imageRoutingTextModel))
		})
	}
	// An endpoint image alias must not be replaced with the hosted-tool default.
	alias := imageRoutingAccount(3, map[string]any{"image-alias": "flux-example"})
	ctx := WithOpenAIImagesEndpoint(WithOpenAIImageGenerationIntent(context.Background()))
	require.Empty(t, openAIImageRequestRejection(ctx, &alias, "image-alias"))
}

func TestOpenAIImageRoutingSchedulersSkipTextOnlyAndCooledAccounts(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
	groupID := int64(9)
	textOnly := imageRoutingAccount(1, map[string]any{imageRoutingTextModel: imageRoutingTextModel})
	cooled := imageRoutingAccount(2, map[string]any{imageRoutingTextModel: imageRoutingTextModel, "gpt-image-2": "gpt-image-2"})
	cooled.Priority = 1
	setAccountModelRateLimitSnapshot(&cooled, openAIImageGenerationRateLimitKey, time.Now().Add(time.Hour), openAIImageCapabilityLossReason, time.Now())
	available := imageRoutingAccount(3, map[string]any{imageRoutingTextModel: imageRoutingTextModel, "gpt-image-2": "gpt-image-2"})
	available.Priority = 2
	textOnly.GroupIDs, cooled.GroupIDs, available.GroupIDs = []int64{groupID}, []int64{groupID}, []int64{groupID}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{textOnly, cooled, available}},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	ctx := WithOpenAIImageGenerationRequest(context.Background(), imageRoutingTextModel, []byte(`{"tools":[{"type":"image_generation"}]}`))
	// The legacy selector used to choose the highest-priority text-only account.
	selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &groupID, "", "", imageRoutingTextModel, nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityResponses, false, false, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, available.ID, selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
	// Advanced candidate filtering must apply the same restriction before TopK.
	scheduler := &defaultOpenAIAccountScheduler{service: svc, stats: newOpenAIAccountRuntimeStats()}
	request := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: imageRoutingTextModel, RequiredCapability: OpenAIEndpointCapabilityResponses}
	require.False(t, scheduler.isAccountRequestCompatible(ctx, &textOnly, request))
	require.False(t, scheduler.isAccountRequestCompatible(ctx, &cooled, request))
	require.True(t, scheduler.isAccountRequestCompatible(ctx, &available, request))
	selection, err = svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", imageRoutingTextModel, nil)
	require.NoError(t, err)
	require.Equal(t, textOnly.ID, selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIImageRoutingAdmissionRechecksEachTurn(t *testing.T) {
	account := imageRoutingAccount(1, map[string]any{imageRoutingTextModel: imageRoutingTextModel})
	svc := &OpenAIGatewayService{accountRepo: &turnAdmissionRepo{account: &account}}
	image := []byte(`{"model":"gpt-6-astra","tools":[{"type":"image_generation"}]}`)
	text := []byte(`{"model":"gpt-6-astra","input":"hello"}`)
	ctx := WithOpenAIImageGenerationRequest(context.Background(), imageRoutingTextModel, image)
	_, err := svc.admitOpenAITurn(ctx, nil, &account, imageRoutingTextModel, image)
	var denied *OpenAITurnAdmissionError
	require.ErrorAs(t, err, &denied)
	require.Equal(t, "image_model_not_supported", denied.Reason)
	// The inherited image context must not reject a later text frame.
	_, err = svc.admitOpenAITurn(ctx, nil, &account, imageRoutingTextModel, text)
	require.NoError(t, err)
	account.Credentials["model_mapping"] = map[string]any{imageRoutingTextModel: imageRoutingTextModel, "gpt-image-2": "gpt-image-2"}
	setAccountModelRateLimitSnapshot(&account, openAIImageGenerationRateLimitKey, time.Now().Add(time.Hour), openAIImageCapabilityLossReason, time.Now())
	_, err = svc.admitOpenAITurnForGroup(context.Background(), 0, false, &account, imageRoutingTextModel, image)
	require.ErrorAs(t, err, &denied)
	require.Equal(t, "image_model_rate_limited", denied.Reason)
	_, err = svc.admitOpenAITurnForGroup(ctx, 0, false, &account, imageRoutingTextModel, text)
	require.NoError(t, err)
}

func TestOpenAIImageRoutingBridgeDoesNotInjectIntoTextOnlyAccount(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.CodexImageGenerationBridgeEnabled = true
	svc := &OpenAIGatewayService{cfg: cfg}
	account := imageRoutingAccount(1, map[string]any{imageRoutingTextModel: imageRoutingTextModel})
	require.False(t, svc.isCodexImageGenerationBridgeEnabled(context.Background(), &account, nil))
	account.Credentials["model_mapping"] = map[string]any{imageRoutingTextModel: imageRoutingTextModel, "gpt-image-2": "gpt-image-2"}
	require.True(t, svc.isCodexImageGenerationBridgeEnabled(context.Background(), &account, nil))
	setAccountModelRateLimitSnapshot(&account, openAIImageGenerationRateLimitKey, time.Now().Add(time.Hour), openAIImageCapabilityLossReason, time.Now())
	require.False(t, svc.isCodexImageGenerationBridgeEnabled(context.Background(), &account, nil))
}

type imagePermissionRepo struct {
	rateLimitAccountRepoStub
	modelCalls int
}

func (r *imagePermissionRepo) SetModelRateLimit(_ context.Context, _ int64, scope string, _ time.Time, _ ...string) error {
	if scope == openAIImageGenerationRateLimitKey {
		r.modelCalls++
	}
	return nil
}

func TestOpenAIImagePermission403NeverDisablesTextAccount(t *testing.T) {
	for _, gateway := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared handler", true: "gateway fastpath"}[gateway], func(t *testing.T) {
			repo := &imagePermissionRepo{}
			counter := &openAI403CounterCacheStub{counts: []int64{3}}
			limits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			limits.SetOpenAI403CounterCache(counter)
			svc := &OpenAIGatewayService{rateLimitService: limits}
			limits.SetAccountRuntimeBlocker(svc)
			account := imageRoutingAccount(1, nil)
			body := []byte(`{"error":{"type":"permission_error","message":"Image generation is not enabled for this group"}}`)
			for range 3 {
				var disabled bool
				if gateway {
					disabled = svc.handleOpenAIAccountUpstreamError(context.Background(), &account, http.StatusForbidden, http.Header{}, body, imageRoutingTextModel)
				} else {
					disabled = limits.HandleUpstreamError(context.Background(), &account, http.StatusForbidden, http.Header{}, body, imageRoutingTextModel)
				}
				require.False(t, disabled)
			}
			require.Equal(t, 3, repo.modelCalls)
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.tempCalls)
			require.Equal(t, []int64{3}, counter.counts, "must not consume the account 403 streak")
			require.False(t, svc.isOpenAIAccountRuntimeBlocked(&account))
			require.True(t, account.IsSchedulableForModelWithContext(context.Background(), imageRoutingTextModel))
		})
	}
}

func TestOpenAIImagePermission403RequiresStructuredEvidence(t *testing.T) {
	for _, body := range []string{
		`{"error":{"type":"permission_error","message":"Account disabled"}}`,
		`{"error":{"type":"content_policy_violation","message":"Image generation is not enabled for this group"}}`,
		`{"input":"Image generation is not enabled for this group"}`,
		`Image generation is not enabled for this group`,
	} {
		require.False(t, isOpenAIImagePermissionError(http.StatusForbidden, []byte(body)))
	}
	valid := []byte(`{"error":{"type":"permission_error","message":"Image generation is not enabled for this group"}}`)
	require.True(t, isOpenAIImageCapabilityLossError(http.StatusForbidden, valid))
	require.False(t, isOpenAIImagePermissionError(http.StatusUnauthorized, valid))
}
