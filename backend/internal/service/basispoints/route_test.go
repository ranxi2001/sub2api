package basispoints

import "testing"

func TestNativeFallbackReason(t *testing.T) {
	cases := []struct {
		body, want string
	}{
		{`{"tools":[{"type":"image_generation"}]}`, "image_generation"},
		{`{"tools":[{"type":"web_search","external_web_access":true}]}`, "web_search"},
		{`{"tools":[{"type":"function","name":"exec"}]}`, ""},
		{`{"tools":[{"type":"web_search","external_web_access":true}],"tool_choice":"none"}`, ""},
		{`{"tool_choice":{"type":"web_search"}}`, "tool_choice"},
		{`{"tool_choice":{"type":"image_generation"}}`, "tool_choice"},
		{`{"tool_choice":{"type":"function","name":"web_search_client"}}`, ""},
		{`{"tool_choice":{"type":"custom","name":"image_generation_client"}}`, ""},
		{`{"input":[{"type":"additional_tools","tools":[{"type":"image_generation"}]}]}`, "image_generation"},
		{`{"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"images","tools":[{"type":"image_generation"}]}]}]}`, "image_generation"},
		{`{"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"image_generation"}]}}`, "image_generation"},
		{`{"tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}],"tool_choice":"auto"}`, ""},
		{`{"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}]}]}`, ""},
		{`{"input":[{"role":"user","content":"explain image_generation tools"}]}`, ""},
		{`{"tools":[{"type":"custom","name":"image_gen.imagegen"}]}`, ""},
		{`{"input":"draw a kitten"}`, ""},
	}
	for _, tc := range cases {
		if got := NativeFallbackReason([]byte(tc.body)); got != tc.want {
			t.Errorf("reason=%q want %q for %s", got, tc.want, tc.body)
		}
	}
}
