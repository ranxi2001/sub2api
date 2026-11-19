//go:build unit && bridgewarmup

package service

import (
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestHTTPBridgeWarmupGuards(t *testing.T) {
	for _, raw := range []string{
		`{"generate":true,"generate":false,"input":[]}`,
		`{"generate":"false","input":[]}`,
		`{"generate":false,"input":[{"role":"user","content":"must not generate"}]}`,
		`{"generate":false,"input":[],"previous_response_id":"resp_foreign"}`,
		`{"generate":false,"input":[],"conversation":"conv_foreign"}`,
		`{"generate":false,"input":[],"prompt":{"id":"fixture"}}`,
		`{"generate":false,"input":[],"input":[1]}`,
	} {
		var w openAIWSHTTPWarmup
		writes := 0
		result, handled, err := w.accept([]byte(raw), "gpt-5.1", 1, func([]byte) error { writes++; return nil })
		require.True(t, handled)
		require.ErrorIs(t, err, ErrOpenAIWSUnsupportedWarmup)
		require.Nil(t, result)
		require.Zero(t, writes)
	}
	var w openAIWSHTTPWarmup
	_, handled, err := w.accept([]byte(`{"generate":false,"input":[]}`), "gpt-5.1", 2, func([]byte) error { t.Fatal("must not write"); return nil })
	require.True(t, handled)
	require.ErrorIs(t, err, ErrOpenAIWSUnsupportedWarmup)
	_, handled, err = w.accept([]byte(`{"generate":true,"input":[]}`), "gpt-5.1", 1, nil)
	require.False(t, handled)
	require.NoError(t, err)
	_, _, err = w.accept([]byte(`{"generate":false,"input":[]}`), "gpt-5.1", 1, func([]byte) error { return io.ErrClosedPipe })
	require.True(t, errors.Is(err, io.ErrClosedPipe))
}

func TestHTTPBridgeWarmupDefaultsAndForeignPointer(t *testing.T) {
	var w openAIWSHTTPWarmup
	_, _, err := w.accept([]byte(`{"generate":false,"model":"gpt-5.1","input":[],"instructions":"old","max_output_tokens":256,"metadata":{"n":9007199254740993}}`), "gpt-5.1", 1, func([]byte) error { return nil })
	require.NoError(t, err)
	next, err := w.nextFrame([]byte(`{"type":"response.create","input":["new"],"instructions":"override","previous_response_id":"resp_foreign"}`), 1<<20)
	require.NoError(t, err)
	require.Equal(t, "override", gjson.GetBytes(next, "instructions").String())
	require.Equal(t, "resp_foreign", gjson.GetBytes(next, "previous_response_id").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(next, "metadata.n").Raw)
	require.False(t, gjson.GetBytes(next, "generate").Exists())
	require.Nil(t, w.defaults)
}

func TestHTTPBridgeWarmupOptionalNulls(t *testing.T) {
	for _, source := range []string{
		`{"generate":false,"model":"gpt-5.1"}`,
		`{"generate":false,"model":"gpt-5.1","input":null,"previous_response_id":null,"conversation":null,"prompt":null}`,
	} {
		var w openAIWSHTTPWarmup
		writes := 0
		result, handled, err := w.accept([]byte(source), "gpt-5.1", 1, func([]byte) error { writes++; return nil })
		require.NoError(t, err)
		require.True(t, handled)
		require.NotNil(t, result)
		require.Equal(t, 2, writes)
	}
}
