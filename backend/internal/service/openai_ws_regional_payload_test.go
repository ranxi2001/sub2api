package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIWSSSEIncompleteRegionalPayloadTypes(t *testing.T) {
	type preparedAlias = openAIWSPreparedJSON
	type rawAlias = json.RawMessage
	type bytesAlias = []byte
	type namedBytes []byte
	type namedPrepared openAIWSPreparedJSON
	raw := []byte("{\"type\":\"response.create\",\"model\":\"mock-text\"}\n")
	prepared := openAIWSPreparedJSON(raw)
	for _, tc := range []struct {
		name  string
		value any
		ok    bool
	}{
		{"map", map[string]any{"type": "response.create", "model": "mock-text"}, true},
		{"prepared", prepared, true},
		{"prepared type alias", preparedAlias(raw), true},
		{"raw message", json.RawMessage(raw), true},
		{"raw message type alias", rawAlias(raw), true},
		{"ordinary bytes", raw, false},
		{"ordinary bytes type alias", bytesAlias(raw), false},
		{"named bytes", namedBytes(raw), false},
		{"new defined prepared type", namedPrepared(raw), false},
		{"prepared pointer", &prepared, false},
		{"nil prepared", openAIWSPreparedJSON(nil), false},
		{"JSON string", string(raw), false},
		{"map model mismatch", map[string]any{"model": "other"}, false},
		{"map forbidden feature", map[string]any{"model": "mock-text", "tools": []any{map[string]any{}}}, false},
		{"malformed raw message", json.RawMessage(`{"model":"mock-text"`), false},
		{"raw message model mismatch", json.RawMessage(`{"model":"other"}`), false},
		{"raw message oversize limit", json.RawMessage(`{"model":"mock-text","max_output_tokens":101}`), false},
		{"raw message forbidden feature", json.RawMessage(`{"model":"mock-text","tools":[{}]}`), false},
		{"raw message alias model mismatch", rawAlias(`{"model":"other"}`), false},
		{"unmarshalable value", make(chan int), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &meterTestLedger{}
			s := &OpenAIGatewayService{regionalMetering: testRegionalMeter(l)}
			body := []byte(`{"model":"mock-text","input":"synthetic","max_output_tokens":10}`)
			c := newOpenAIRejectedFieldTestContext(body)
			c.Set("api_key", &APIKey{ID: 4, UserID: 5})
			account := newOpenAIRejectedFieldTestAccount()
			account.Type = AccountTypeOAuth
			ctx, _, err := s.beginRegionalMeter(t.Context(), c, account, body)
			require.NoError(t, err)
			err = validateOpenAIWSRegionalPayload(ctx, tc.value)
			if tc.ok {
				require.NoError(t, err, "accepted values still pass the real regional validator")
			} else {
				require.Error(t, err, "an unrelated value must not gain the prepared bytes fast path")
			}
			require.Zero(t, l.starts, "validation must not start or submit a request")
		})
	}
	// The new boundary does not marshal or reject values on non-regional sends.
	require.NoError(t, validateOpenAIWSRegionalPayload(context.Background(), make(chan int)))
}

func TestOpenAIWSSSEIncompleteRegionalPreparedPayloadBounds(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		ok        bool
	}{
		{"full OAuth window", `{"model":"mock-text"}`, true},
		{"explicit limit", `{"model":"mock-text","max_output_tokens":10}`, true},
		{"ceiling limit", `{"model":"mock-text","max_output_tokens":100}`, true},
		{"legacy limit", `{"model":"mock-text","max_tokens":10}`, true},
		{"empty tools", `{"model":"mock-text","tools":[]}`, true},
		{"null features", `{"model":"mock-text","tools":null,"previous_response_id":null,"conversation":null,"service_tier":null}`, true},
		{"empty bytes", ``, false},
		{"missing closing brace", `{"model":"mock-text"`, false},
		{"trailing garbage", `{"model":"mock-text"} invalid`, false},
		{"null", `null`, false},
		{"array", `[{"model":"mock-text"}]`, false},
		{"model mismatch", `{"model":"other"}`, false},
		{"zero limit", `{"model":"mock-text","max_output_tokens":0}`, false},
		{"negative limit", `{"model":"mock-text","max_output_tokens":-1}`, false},
		{"oversize limit", `{"model":"mock-text","max_output_tokens":101}`, false},
		{"string limit", `{"model":"mock-text","max_output_tokens":"10"}`, false},
		{"tools", `{"model":"mock-text","tools":[{}]}`, false},
		{"previous response", `{"model":"mock-text","previous_response_id":"resp_other"}`, false},
		{"conversation", `{"model":"mock-text","conversation":"conv_other"}`, false},
		{"service tier", `{"model":"mock-text","service_tier":"priority"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &meterTestLedger{}
			s := &OpenAIGatewayService{regionalMetering: testRegionalMeter(l)}
			body := []byte(`{"model":"mock-text","input":"synthetic","max_output_tokens":10}`)
			c := newOpenAIRejectedFieldTestContext(body)
			c.Set("api_key", &APIKey{ID: 4, UserID: 5})
			account := newOpenAIRejectedFieldTestAccount()
			account.Type = AccountTypeOAuth
			ctx, _, err := s.beginRegionalMeter(t.Context(), c, account, body)
			require.NoError(t, err)
			prepared := openAIWSPreparedJSON(tc.raw)
			err = validateOpenAIWSRegionalPayload(ctx, prepared)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err, "prepared bytes must not bypass JSON syntax or regional cost bounds")
			}
			if tc.name == "full OAuth window" {
				regionalMeterAttemptFrom(ctx).fullOutputWindow = false
				require.Error(t, validateOpenAIWSRegionalPayload(ctx, prepared), "missing output limit requires the full-window reservation")
			}
			require.Equal(t, tc.raw, string(prepared), "validation must not change the bytes passed to the real WS writer")
			require.Zero(t, l.starts)
		})
	}
}
