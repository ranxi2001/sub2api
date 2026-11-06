package service

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// This reference preserves the pre-optimization control flow for differential tests.
func explicitImageIntentReference(endpoint, requestedModel string, body []byte) bool {
	if IsImageGenerationEndpoint(endpoint) || isOpenAIImageGenerationModel(requestedModel) {
		return true
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	var modelSeen, toolsSeen, toolChoiceSeen bool
	imageIntent := false
	parseRawJSONView(body).ForEach(func(key, value gjson.Result) bool {
		switch key.Str {
		case "model":
			if !modelSeen {
				modelSeen = true
				imageIntent = isOpenAIImageGenerationModel(strings.TrimSpace(value.String()))
			}
		case "tools":
			if !toolsSeen {
				toolsSeen = true
				imageIntent = openAIJSONToolsContainNativeImageGeneration(value)
			}
		case "tool_choice":
			if !toolChoiceSeen {
				toolChoiceSeen = true
				imageIntent = openAIJSONToolChoiceSelectsExplicitImageGeneration(value)
			}
		}
		return !imageIntent && (!modelSeen || !toolsSeen || !toolChoiceSeen)
	})
	return imageIntent
}

func TestExplicitImageIntentCandidateFilter(t *testing.T) {
	tests := []struct {
		name, body string
		candidate  bool
	}{
		{"ordinary", `{"model":"gpt-5","input":"hello"}`, false},
		{"passive namespace", `{"tools":[{"type":"namespace","name":"image_gen"}]}`, false},
		{"passive namespace and auto choice", `{"tools":[{"type":"namespace","name":"image_gen"}],"tool_choice":"auto"}`, false},
		{"native tool", `{"tools":[{"type":"image_generation"}]}`, true},
		{"explicit namespace choice", `{"tool_choice":{"namespace":"image_gen","name":"imagegen"}}`, true},
		{"nested explicit function choice", `{"tool_choice":{"tool":{"function":{"name":"image_gen.imagegen"}}}}`, true},
		{"case-insensitive image model", `{"model":"GPT-IMAGE-2"}`, true},
		{"escaped tool key", `{"tool_\u0063hoice":{"namespace":"image_gen","name":"imagegen"}}`, true},
		{"escaped native type", `{"tools":[{"type":"image_\u0067eneration"}]}`, true},
		{"malformed positive", `{"model":"gpt-image-2"`, true},
		{"malformed ordinary", `{"model":"gpt-5"`, false},
		{"duplicate choice uses first", `{"tool_choice":"auto","tool_choice":{"namespace":"image_gen","name":"imagegen"}}`, false},
		{"duplicate model uses first", `{"model":"gpt-5","model":"gpt-image-2"}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			if got := mayContainExplicitImageGenerationIntent(body); got != tc.candidate {
				t.Fatalf("candidate=%v, want %v", got, tc.candidate)
			}
			if got, want := IsExplicitImageGenerationIntent("/v1/responses", "gpt-5", body), explicitImageIntentReference("/v1/responses", "gpt-5", body); got != want {
				t.Fatalf("intent=%v, reference=%v", got, want)
			}
		})
	}
}

func TestExplicitImageIntentStructuredDifferential(t *testing.T) {
	models := []string{
		`"model":"gpt-5"`, `"model":"gpt-image-2"`, `"model":"GPT-IMAGE-2"`,
		`"model":"GROK-IMAGINE"`, `"model":"gpt-5","model":"gpt-image-2"`,
		`"mo\u0064el":"\u0067pt-image-2"`, `"model":null`,
	}
	tools := []string{
		`"tools":[]`, `"tools":[{"type":"namespace","name":"image_gen"}]`,
		`"tools":[{"type":"image_generation"}]`, `"tools":[{"type":"image_\u0067eneration"}]`,
		`"tools":[],"tools":[{"type":"image_generation"}]`,
	}
	choices := []string{
		`"tool_choice":"auto"`, `"tool_choice":"image_generation"`,
		`"tool_choice":{"type":"namespace","name":"image_gen"}`,
		`"tool_choice":{"namespace":"image_gen","name":"imagegen"}`,
		`"tool_choice":{"function":{"name":"image_gen.imagegen"}}`,
		`"tool_choice":"auto","tool_choice":{"namespace":"image_gen","name":"imagegen"}`,
		`"tool_\u0063hoice":"image_generation"`,
		`"tool_choice":{"tool":{"namespace":"image_gen","name":"imagegen"}}`,
	}
	for _, model := range models {
		for _, tool := range tools {
			for _, choice := range choices {
				for _, fields := range [][]string{
					{model, tool, choice, `"input":"ordinary"`},
					{`"input":"ordinary"`, choice, tool, model},
				} {
					valid := "{" + strings.Join(fields, ",") + "}"
					for _, raw := range []string{valid, valid[:len(valid)-1]} {
						body := []byte(raw)
						got := IsExplicitImageGenerationIntent("/v1/responses", "gpt-5", body)
						want := explicitImageIntentReference("/v1/responses", "gpt-5", body)
						if got != want {
							t.Fatalf("intent=%v, reference=%v for %s", got, want, raw)
						}
					}
				}
			}
		}
	}
}

func FuzzExplicitImageIntentMatchesReference(f *testing.F) {
	for _, body := range []string{
		`{"model":"gpt-5","tools":[{"type":"namespace","name":"image_gen"}],"tool_choice":"auto"}`,
		`{"model":"gpt-image-2"}`,
		`{"tools":[{"type":"image_generation"}]}`,
		`{"tool_choice":{"namespace":"image_gen","name":"imagegen"}}`,
		`{"tool_\u0063hoice":{"type":"image_generation"}}`,
		`{"tool_choice":"auto","tool_choice":{"type":"image_generation"}}`,
		`{"model":"gpt-5","input":[`,
	} {
		f.Add("/v1/responses", "gpt-5", []byte(body))
	}
	f.Add("/v1/images/generations", "gpt-5", []byte(`{`))
	f.Add("/v1/responses", "gpt-image-2", []byte(`{`))
	f.Fuzz(func(t *testing.T, endpoint, model string, body []byte) {
		if len(endpoint)+len(model)+len(body) > 64<<10 {
			t.Skip()
		}
		if got, want := IsExplicitImageGenerationIntent(endpoint, model, body), explicitImageIntentReference(endpoint, model, body); got != want {
			t.Fatalf("intent=%v, reference=%v", got, want)
		}
	})
}

func BenchmarkExplicitImageIntentPassiveNamespace(b *testing.B) {
	for _, suffix := range []bool{false, true} {
		name := "choice_before_input"
		body := []byte(`{"model":"gpt-5","tools":[{"type":"namespace","name":"image_gen"}],`)
		if !suffix {
			body = append(body, `"tool_choice":"auto",`...)
		} else {
			name = "choice_after_input"
		}
		body = append(body, `"input":"`...)
		body = append(body, bytes.Repeat([]byte("x"), 8<<20)...)
		body = append(body, '"')
		if suffix {
			body = append(body, `,"tool_choice":"auto"`...)
		}
		body = append(body, '}')
		for _, variant := range []struct {
			name string
			fn   func(string, string, []byte) bool
		}{
			{"reference", explicitImageIntentReference},
			{"candidate", IsExplicitImageGenerationIntent},
		} {
			b.Run(name+"/"+variant.name, func(b *testing.B) {
				b.SetBytes(int64(len(body)))
				for i := 0; i < b.N; i++ {
					if variant.fn("/v1/responses", "gpt-5", body) {
						b.Fatal("passive namespace is not explicit image intent")
					}
				}
			})
		}
	}
}

func BenchmarkExplicitImageIntentLongHistory(b *testing.B) {
	entry := []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"Synthetic code context and ordinary conversation."}]},`)
	for _, passiveNamespace := range []bool{false, true} {
		name := "plain"
		tools := `"tools":[]`
		if passiveNamespace {
			name = "passive_namespace"
			tools = `"tools":[{"type":"namespace","name":"image_gen"}]`
		}
		body := []byte(`{"model":"gpt-5",` + tools + `,"input":[`)
		body = append(body, bytes.Repeat(entry, 65536)...)
		body = append(body[:len(body)-1], `],"tool_choice":"auto"}`...)
		for _, variant := range []struct {
			name string
			fn   func(string, string, []byte) bool
		}{
			{"reference", explicitImageIntentReference},
			{"candidate", IsExplicitImageGenerationIntent},
		} {
			b.Run(name+"/"+variant.name, func(b *testing.B) {
				b.SetBytes(int64(len(body)))
				for i := 0; i < b.N; i++ {
					if variant.fn("/v1/responses", "gpt-5", body) {
						b.Fatal("passive namespace is not explicit image intent")
					}
				}
			})
		}
	}
}

func BenchmarkExplicitImageIntentFallback(b *testing.B) {
	for _, scenario := range []struct {
		name, prefix, suffix string
		want                 bool
	}{
		{"escaped_unicode", `{"model":"gpt-5","input":"`, `\u0061"}`, false},
		{"text_marker", `{"model":"gpt-5","input":"`, `image_generation"}`, false},
		{"native_tool", `{"model":"gpt-5","tools":[{"type":"image_generation"}],"input":"`, `"}`, true},
	} {
		body := []byte(scenario.prefix)
		body = append(body, bytes.Repeat([]byte("x"), 8<<20)...)
		body = append(body, scenario.suffix...)
		for _, variant := range []struct {
			name string
			fn   func(string, string, []byte) bool
		}{
			{"reference", explicitImageIntentReference},
			{"candidate", IsExplicitImageGenerationIntent},
		} {
			b.Run(scenario.name+"/"+variant.name, func(b *testing.B) {
				b.SetBytes(int64(len(body)))
				for i := 0; i < b.N; i++ {
					if got := variant.fn("/v1/responses", "gpt-5", body); got != scenario.want {
						b.Fatalf("intent=%v, want %v", got, scenario.want)
					}
				}
			})
		}
	}
}
