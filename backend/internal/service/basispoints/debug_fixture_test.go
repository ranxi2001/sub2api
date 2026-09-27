package basispoints

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func deliveryFixturePath(t *testing.T, name string) string {
	t.Helper()
	dir := os.Getenv("BPS_DELIVERY_FIXTURE_DIR")
	if dir == "" {
		t.Skip("set BPS_DELIVERY_FIXTURE_DIR to run the private production fixture")
	}
	return filepath.Join(dir, name)
}

func TestDebugRealDeliveryFixtureCompaction(t *testing.T) {
	raw, err := os.ReadFile(deliveryFixturePath(t, "bps-image-file-delivery-client-call.json"))
	if err != nil {
		t.Fatal(err)
	}
	var call object
	if err := json.Unmarshal(raw, &call); err != nil {
		t.Fatal(err)
	}
	payload, payloadOK := imageFilePayload(call)
	compact, compactOK := compactImageRenderingCall(call)
	if !payloadOK || !compactOK {
		t.Fatal("delivery fixture must be recognized and compacted")
	}
	t.Logf("input_len=%d payload_ok=%v compact_ok=%v prefix_v1=%v prefix_v2=%v", len(text(call["input"])), payloadOK, compactOK, len(text(call["input"])) > len(imageFileDeliveryPrefix) && text(call["input"])[:len(imageFileDeliveryPrefix)] == imageFileDeliveryPrefix, len(text(call["input"])) > len(imageFileDeliveryV2Prefix) && text(call["input"])[:len(imageFileDeliveryV2Prefix)] == imageFileDeliveryV2Prefix)
	if payloadOK {
		t.Logf("payload_keys=%v sha=%s png_len=%d", len(payload), text(payload["sha256"]), len(text(payload["png_b64"])))
	}
	if compactOK {
		t.Logf("compact_input_len=%d", len(text(compact["input"])))
	}
}

func TestDebugRealDeliveryFixtureWire(t *testing.T) {
	rawCall, err := os.ReadFile(deliveryFixturePath(t, "bps-image-file-delivery-client-call.json"))
	if err != nil {
		t.Fatal(err)
	}
	rawReceipt, err := os.ReadFile(deliveryFixturePath(t, "bps-image-file-delivery-client-receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var call, receipt object
	if err := json.Unmarshal(rawCall, &call); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawReceipt, &receipt); err != nil {
		t.Fatal(err)
	}
	source := fileDeliveryTestSource()
	source["input"] = []any{message("user", "make an image"), call, receiptResult(t, call, receipt)}
	wire, bridge := mustPrepare(t, source, "account/key/thread", nil)
	rawWire, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := imageFilePayload(call)
	if !ok || bytes.Contains(rawWire, []byte(text(payload["png_b64"]))) {
		t.Fatal("image payload leaked into compacted wire history")
	}
	t.Logf("wire_bytes=%d bridge_files=%v contains_png=false", len(rawWire), bridge.imageFiles)
	if len(rawWire) > 1024*1024 {
		t.Fatalf("wire remains over 1 MiB: %d", len(rawWire))
	}
}
