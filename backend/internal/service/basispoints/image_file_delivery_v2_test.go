package basispoints

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageFileDeliveryV2DoesNotReuploadPreview(t *testing.T) {
	call, receipt, file := fileDeliveryFixture(t)
	require.Contains(t, text(call["input"]), imageFileDeliveryV2Prefix)
	require.NotContains(t, imageFileDeliveryV2JS, "tools.view_image")
	require.NotContains(t, imageFileDeliveryV2JS, "image(preview")
	source := fileDeliveryTestSource()
	source["input"] = []any{message("user", "make image"), call, receiptResult(t, call, receipt)}
	_, bridge := mustPrepare(t, source, "account/key/thread", nil)
	_, response := imageTestStream(t, bridge, []any{message("assistant", "完成。")})
	require.Contains(t, text(response["output"].([]any)[1].(object)["content"].([]any)[0].(object)["text"]), file)
}

func TestImageFileDeliveryV1ReceiptsAndHistoryRemainValid(t *testing.T) {
	call, receipt, file := fileDeliveryFixture(t)
	payload, ok := imageFilePayload(call)
	require.True(t, ok)
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	call["input"] = imageFileDeliveryPrefix + imageFileDeliveryJS + ")(" + string(raw) + ");"
	_, ok = imageFilePayload(call)
	require.True(t, ok)
	bridge := &Bridge{}
	bridge.collectImageFileReceipts([]any{call, receiptResult(t, call, receipt)})
	require.Equal(t, []string{file}, bridge.imageFiles)
	compact, ok := compactImageRenderingCall(call)
	require.True(t, ok)
	require.NotContains(t, text(compact["input"]), text(payload["png_b64"]))
	call["input"] = text(call["input"]) + "\ntext('tampered');"
	_, ok = imageFilePayload(call)
	require.False(t, ok)
}
