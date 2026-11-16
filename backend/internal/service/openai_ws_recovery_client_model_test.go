//go:build unit && inheritedmodel

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Client scope must follow the current accepted frame across explicit model
// changes, even when both aliases map to the same upstream model. Normalizing
// proof metadata must never change immutable client input or the forward scope.
func TestOpenAIWSRecoveryEffectiveClientModel(t *testing.T) {
	for _, scenario := range []string{"no_ws_preflight", "inherited", "explicit_switch", "inherited_after_switch", "explicit_overrides_preflight", "separate_connection"} {
		t.Run(scenario, func(t *testing.T) {
			c, _ := newTurnStateTestContext(t, 715, "fixture-session")
			svc := &OpenAIGatewayService{}
			body := []byte(`{"input":[{"role":"user","content":"fixture scope"}]}`)
			want := ""
			if scenario != "no_ws_preflight" {
				SetOpenAIWSRecoveryClientModel(c, "client-first")
				want = "client-first"
			}
			switch scenario {
			case "explicit_switch", "inherited_after_switch":
				SetOpenAIWSRecoveryClientModel(c, "client-second")
				want = "client-second"
				if scenario == "explicit_switch" {
					body = []byte(`{"model":"client-second","input":[]}`)
				}
			case "explicit_overrides_preflight":
				body = []byte(`{"model":"client-explicit","input":[]}`)
				want = "client-explicit"
			case "separate_connection":
				c, _ = newTurnStateTestContext(t, 715, "fixture-session")
				want = ""
			}
			before := string(body)
			ctx := svc.WithOpenAIAgentMessageRecovery(context.Background(), c, body, "fixture-session", "mapped-upstream")
			request := openAIAgentRecoveryFromContext(ctx)
			require.Equal(t, want, request.shadowModel)
			require.Equal(t, "mapped-upstream", request.model)
			require.Equal(t, before, string(body), "the source frame must remain byte-identical")
		})
	}
}
