package qqbot

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// Opt-in paid evaluation: never reads production configuration and never calls
// an image endpoint or QQ. Mocks cannot establish a real model's intent quality.
func TestQQBotImageIntentSemanticEval(t *testing.T) {
	if os.Getenv("QQBOT_IMAGE_INTENT_EVAL") != "1" {
		t.Skip("opt-in paid semantic evaluation; set QQBOT_IMAGE_INTENT_EVAL=1 and dedicated evaluation credentials")
	}
	c := DefaultConfig()
	c.BaseURL, c.APIKey, c.ChatModel = os.Getenv("QQBOT_IMAGE_INTENT_EVAL_BASE_URL"), os.Getenv("QQBOT_IMAGE_INTENT_EVAL_API_KEY"), os.Getenv("QQBOT_IMAGE_INTENT_EVAL_MODEL")
	require.NotEmpty(t, c.BaseURL)
	require.NotEmpty(t, c.APIKey)
	require.NotEmpty(t, c.ChatModel)
	require.NoError(t, validateURL(c.BaseURL))
	if protocol := os.Getenv("QQBOT_IMAGE_INTENT_EVAL_PROTOCOL"); protocol != "" {
		c.ChatProtocol = protocol
	}
	require.Contains(t, []string{"chat_completions", "responses"}, c.ChatProtocol)
	s := New(nil, nil, false, nil)
	plan := &pendingImagePlan{Action: chatImageToolName, Prompt: "蓝紫色猫咪海报", RequestQuote: "设计一张猫咪海报"}
	cases := []struct {
		name, current, proposal, want, basis string
		pending                              *pendingImagePlan
	}{
		{"identity", "现在你还会骂我吗？我真的是Dean啊", "Dean 求饶的搞笑表情包", "text", "none", nil},
		{"greeting", "下午好🌹", "下午好玫瑰祝福卡片", "text", "none", nil},
		{"capability", "你能画图吗？", "展示绘画能力的小猫图", "text", "none", nil},
		{"appreciation", "这个不错", "蓝紫色猫咪海报", "text", "none", plan},
		{"prompt-only", "写一段画猫的提示词，不要生成图片", "一只猫", "text", "none", nil},
		{"cancel", "取消，不要图片", "蓝紫色猫咪海报", "text", "none", plan},
		{"explicit", "帮我画一只猫", "一只猫", "execute", "explicit", nil},
		{"confirmation", "就按刚才的方案来一张", "蓝紫色猫咪海报", "execute", "continuation", plan},
		{"missing-plan", "就按刚才的方案来一张", "蓝紫色猫咪海报", "clarify", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			review, usage, err := s.reviewImageIntent(context.Background(), c, Event{}, tc.current, Message{}, nil, tc.pending, chatImageToolName, tc.proposal, 0)
			require.NoError(t, err)
			require.Equal(t, tc.want, review.Disposition)
			if tc.basis != "" {
				require.Equal(t, tc.basis, review.Basis)
			}
			if tc.want == "execute" {
				require.NotEmpty(t, review.Evidence)
				require.Contains(t, tc.current, review.Evidence)
			}
			t.Logf("decision=%s basis=%s input_tokens=%d output_tokens=%d", review.Disposition, review.Basis, usage.InputTokens, usage.OutputTokens)
		})
	}
}
