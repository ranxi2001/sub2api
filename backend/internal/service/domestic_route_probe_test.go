package service

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDomesticRouteThreshold(t *testing.T) {
	for _, c := range []struct {
		elapsed time.Duration
		on      bool
	}{
		{-time.Millisecond, false}, {0, true}, {3*time.Second - time.Nanosecond, true}, {3 * time.Second, false}, {4 * time.Second, false},
	} {
		require.Equal(t, c.on, domesticRouteFast(c.elapsed))
	}
}

func TestDomesticRouteRequiresVisibleText(t *testing.T) {
	for _, c := range []struct {
		protocol, body string
		visible        bool
	}{
		{APIProtocolChatCompletions, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"thinking\"}}]}\n\ndata: [DONE]\n", false},
		{APIProtocolChatCompletions, ": ping\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n", true},
		{APIProtocolResponses, "data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"thought\"}\n", false},
		{APIProtocolResponses, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n", true},
		{APIProtocolAnthropic, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"OK\"}}\n", true},
		{APIProtocolAnthropic, "data: {\"type\":\"error\"}\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n", false},
	} {
		require.Equal(t, c.visible, domesticRouteVisibleText(strings.NewReader(c.body), c.protocol, nil, ""))
	}
}

func TestDomesticRouteUsesAccountMapping(t *testing.T) {
	a := &Account{Type: AccountTypeAPIKey, Platform: PlatformDeepseek, Credentials: map[string]any{"model_mapping": map[string]any{"deepseek-flash": "deepseek-v4.1-flash"}}}
	require.Equal(t, "deepseek-v4.1-flash", domesticRouteModel(a))
	video := &Account{Type: AccountTypeAPIKey, Platform: PlatformQwen, Credentials: map[string]any{"model_mapping": map[string]any{"wan3.0-video": "wan3.0-video"}}}
	require.Empty(t, domesticRouteModel(video))
}
