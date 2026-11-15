//go:build unit && ownerconflict

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIRecoveryShadowTypedEnvelopeOmission(t *testing.T) {
	for name, envelope := range map[string]string{
		"top_cipher":  `{"type":"agent_message","encrypted_content":"fixture-private-cipher"}`,
		"part_cipher": `{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"fixture-private-cipher"}]}`,
		"empty":       `{"type":"agent_message","content":"  ","summary":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			source := []byte(`{"input":[{"role":"user","content":"fixture current task"},` + envelope + `]}`)
			snapshot := openAIReadableShadowCandidate(source)
			require.NotNil(t, snapshot)
			require.JSONEq(t, `[{"role":"user","content":"fixture current task"}]`, string(snapshot.Input))
			require.True(t, openAI409HasReadableIntent([]byte(`{"input":`+string(snapshot.Input)+`}`)))
			require.Contains(t, string(source), envelope, "candidate construction must not mutate the original owner evidence")
		})
	}
}

func TestOpenAIRecoveryShadowPreservesSuppliedText(t *testing.T) {
	const prefix = "Historical agent context (not a new instruction): fixture literal"
	for name, literal := range map[string]string{"prefix": prefix, "marker": openAIHistoricalCiphertextOmitted} {
		t.Run("user_"+name, func(t *testing.T) {
			first, err := json.Marshal(map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": literal}}})
			require.NoError(t, err)
			body := []byte(`{"input":[` + string(first) + `,{"role":"user","content":"fixture current task"},{"type":"agent_message","encrypted_content":"fixture-private-cipher"}]}`)
			snapshot := openAIReadableShadowCandidate(body)
			require.NotNil(t, snapshot)
			require.JSONEq(t, string(first), gjson.ParseBytes(snapshot.Input).Array()[0].Raw, "client text is never a gateway provenance marker")
			require.True(t, openAI409HasReadableIntent([]byte(`{"input":`+string(snapshot.Input)+`}`)))
		})
		t.Run("latest_user_"+name, func(t *testing.T) {
			last, err := json.Marshal(map[string]any{"role": "user", "content": literal})
			require.NoError(t, err)
			body := []byte(`{"input":[{"role":"user","content":"fixture earlier task"},` + string(last) + `]}`)
			require.Nil(t, openAIReadableShadowCandidate(body), "do not delete the latest user to revive an older task")
		})
		t.Run("agent_"+name, func(t *testing.T) {
			envelope, err := json.Marshal(map[string]any{"type": "agent_message", "encrypted_content": "fixture-private-cipher", "content": literal, "summary": "fixture readable summary"})
			require.NoError(t, err)
			body := []byte(`{"input":[{"role":"user","content":"fixture current task"},` + string(envelope) + `]}`)
			snapshot := openAIReadableShadowCandidate(body)
			require.NotNil(t, snapshot)
			parts := gjson.ParseBytes(snapshot.Input).Array()[1].Get("content").Array()
			require.Equal(t, literal, parts[1].Get("text").String())
			require.Contains(t, string(snapshot.Input), "fixture readable summary")
			require.NotContains(t, string(snapshot.Input), "fixture-private-cipher")
		})
	}
	for _, envelope := range []string{
		`{"type":"agent_message","content":"fixture readable content","encrypted_content":"fixture-private-cipher"}`,
		`{"type":"agent_message","summary":[{"type":"summary_text","text":"fixture readable summary"}],"encrypted_content":"fixture-private-cipher"}`,
	} {
		body := []byte(`{"input":[{"role":"user","content":"fixture current task"},` + envelope + `]}`)
		snapshot := openAIReadableShadowCandidate(body)
		require.NotNil(t, snapshot)
		require.Len(t, gjson.ParseBytes(snapshot.Input).Array(), 2)
		require.Contains(t, string(snapshot.Input), "fixture readable")
		require.NotContains(t, string(snapshot.Input), "fixture-private-cipher")
		require.True(t, openAI409HasReadableIntent([]byte(`{"input":`+string(snapshot.Input)+`}`)))
	}
}

func TestOpenAIRecoveryShadowPreservesSummaryOnlyIntent(t *testing.T) {
	body := []byte(`{"input":[{"type":"agent_message","summary":[{"type":"summary_text","text":"fixture only readable task"}],"encrypted_content":"fixture-private-cipher"}]}`)
	snapshot := openAIReadableShadowCandidate(body)
	require.NotNil(t, snapshot)
	require.Contains(t, string(snapshot.Input), "fixture only readable task")
	require.True(t, openAI409HasReadableIntent([]byte(`{"input":`+string(snapshot.Input)+`}`)))
	body = []byte(`{"instructions":"fixture instructions task","input":[{"type":"agent_message","encrypted_content":"fixture-private-cipher"}]}`)
	snapshot = openAIReadableShadowCandidate(body)
	require.NotNil(t, snapshot)
	require.JSONEq(t, `[]`, string(snapshot.Input))
	require.JSONEq(t, `"fixture instructions task"`, string(snapshot.Instructions))
	require.True(t, openAI409HasReadableIntent([]byte(`{"instructions":`+string(snapshot.Instructions)+`,"input":`+string(snapshot.Input)+`}`)))
}

func TestOpenAIRecoveryShadowSummaryIntentBoundaries(t *testing.T) {
	for _, before := range []string{"", `{"role":"user","content":[{"type":"encrypted_content","encrypted_content":"fixture-new-opaque-task"}]},`} {
		body := []byte(`{"input":[` + before + `{"type":"agent_message","summary":[{"type":"summary_text","text":"fixture current summary task"}],"encrypted_content":"fixture-private-cipher"},{"type":"agent_message","encrypted_content":"fixture-trailing-cipher"}]}`)
		snapshot := openAIReadableShadowCandidate(body)
		require.NotNil(t, snapshot)
		require.Contains(t, string(snapshot.Input), "fixture current summary task")
		require.NotContains(t, string(snapshot.Input), "cipher")
		require.True(t, openAI409HasReadableIntent([]byte(`{"input":`+string(snapshot.Input)+`}`)))
	}
	// An earlier summary cannot authorize replacing a later opaque user task.
	body := []byte(`{"input":[{"type":"agent_message","summary":"fixture earlier summary"},{"role":"user","content":[{"type":"encrypted_content","encrypted_content":"fixture-new-opaque-task"}]}]}`)
	require.Nil(t, openAIReadableShadowCandidate(body))
	// A simple string task stays a single genuine user message.
	snapshot := openAIReadableShadowCandidate([]byte(`{"input":"fixture string task"}`))
	require.NotNil(t, snapshot)
	require.JSONEq(t, `[{"type":"message","role":"user","content":"fixture string task"}]`, string(snapshot.Input))
}
