package qqbot

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// Decode the wire payload and reject combinations QQ does not render as intended.
// In particular, accepting a mention tag in plain content hid the original bug.
type qqTestReply struct {
	MsgType  int              `json:"msg_type"`
	Content  string           `json:"content"`
	MsgID    string           `json:"msg_id"`
	EventID  string           `json:"event_id"`
	Sequence int              `json:"msg_seq"`
	Keyboard *messageKeyboard `json:"keyboard"`
	Markdown *struct {
		Content string `json:"content"`
	} `json:"markdown"`
	Media *struct {
		FileInfo string `json:"file_info"`
	} `json:"media"`
}

func readQQTestReply(t *testing.T, r *http.Request) qqTestReply {
	t.Helper()
	var raw map[string]json.RawMessage
	require.NoError(t, json.NewDecoder(r.Body).Decode(&raw))
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	var reply qqTestReply
	require.NoError(t, json.Unmarshal(data, &reply))
	if reply.EventID != "" {
		require.NotContains(t, raw, "msg_id")
	}
	switch reply.MsgType {
	case 0:
		require.Contains(t, raw, "content")
		require.NotContains(t, raw, "markdown")
		require.NotContains(t, raw, "media")
		require.NotContains(t, reply.Content, "<qqbot-at-user")
	case 2:
		require.NotContains(t, raw, "content")
		require.NotContains(t, raw, "ark")
		require.NotContains(t, raw, "media")
		require.NotNil(t, reply.Markdown)
		require.NotEmpty(t, reply.Markdown.Content)
	case 7:
		require.NotContains(t, raw, "content")
		require.NotContains(t, raw, "markdown")
		require.NotNil(t, reply.Media)
		require.NotEmpty(t, reply.Media.FileInfo)
	default:
		t.Errorf("unexpected QQ message type: %d", reply.MsgType)
	}
	return reply
}

func (r qqTestReply) text() string {
	if r.Markdown != nil {
		return r.Markdown.Content
	}
	return r.Content
}
