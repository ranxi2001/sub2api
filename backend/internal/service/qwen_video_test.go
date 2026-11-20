//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWanVideoInputAndUsage(t *testing.T) {
	in, info, err := parseWanCreate([]byte(`{"model":"wan3.0-video","prompt":"fixture","seconds":"2","size":"832x480","media":[{"type":"first_frame","url":"https://example.com/image.png"}]}`))
	require.NoError(t, err)
	require.Equal(t, "480P", in.Parameters.Resolution)
	require.Equal(t, 2, in.Parameters.Duration)
	require.Equal(t, []string{"https://example.com/image.png"}, info.InputImageURLs)
	for _, data := range []string{
		`{"model":"wan3.0-video","prompt":"fixture","duration":31}`,
		`{"model":"wan3.0-video","prompt":"fixture","seconds":2,"duration":3}`,
		`{"model":"wan3.0-video","prompt":"fixture","resolution":"480p","size":"1280x720"}`,
	} {
		_, _, err = parseWanCreate([]byte(data))
		require.Error(t, err)
	}
	task, err := parseWanTask([]byte(`{"output":{"task_id":"task-123","task_status":"SUCCEEDED","video_url":"https://example.com/movie.mp4"},"usage":{"duration":7.25,"input_video_duration":2.25,"output_video_duration":5,"SR":720}}`), true)
	require.NoError(t, err)
	require.Equal(t, 7.25, task.BilledSeconds)
	require.Equal(t, "720p", task.Resolution)
	task.ID = "video_proxy_fixture"
	b, err := json.Marshal(task)
	require.NoError(t, err)
	relay, err := parseWanTask(b, false)
	require.NoError(t, err)
	require.Equal(t, task.BilledSeconds, relay.BilledSeconds)
	require.Equal(t, task.ID, relay.upstreamID)
	for _, data := range []string{
		`{"output":{"task_id":"task-123","task_status":"SUCCEEDED","video_url":"https://example.com/a"},"usage":{"SR":720}}`,
		`{"output":{"task_id":"task-123","task_status":"SUCCEEDED","video_url":"http://example.com/a"},"usage":{"SR":720,"duration":3}}`,
	} {
		_, err = parseWanTask([]byte(data), true)
		require.Error(t, err)
	}
}

func TestWanFixedDurationSupplierCompletion(t *testing.T) {
	raw := []byte(`{"id":"task-123","status":"completed","video_url":"https://media.example.com/a.mp4"}`)
	_, err := wanFixedDurationReceipt(raw, GrokMediaRequestInfo{})
	require.Error(t, err)
	receipt, err := wanFixedDurationReceipt(raw, GrokMediaRequestInfo{DurationSeconds: 2, Resolution: "480p"})
	require.NoError(t, err)
	task, err := parseWanTask(receipt, false)
	require.NoError(t, err)
	require.Equal(t, 2.0, task.BilledSeconds)
	require.Equal(t, "480p", task.Resolution)
}

func TestWanFixedDurationTextOnlyWireBody(t *testing.T) {
	input, _, err := parseWanCreate([]byte(`{"model":"wan3.0-video","prompt":"fixture","seconds":2,"resolution":"480p"}`))
	require.NoError(t, err)
	wire, err := input.fixedDurationBody()
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"wan3.0-video","prompt":"fixture","duration":2,"resolution":"480P"}`, string(wire))
}
