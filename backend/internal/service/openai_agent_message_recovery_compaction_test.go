//go:build unit

package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithoutOpenAIRemoteCompactionV2FeatureKeepsOtherWireIdentity(t *testing.T) {
	header := http.Header{}
	header.Set("Authorization", "Bearer fixture")
	header["x-codex-beta-features"] = []string{"alpha, remote_compaction_v2 ,beta", "remote_compaction_v2"}

	stripped := withoutOpenAIRemoteCompactionV2Feature(header)

	require.Equal(t, []string{"alpha,beta"}, stripped["x-codex-beta-features"])
	require.Equal(t, "Bearer fixture", stripped.Get("Authorization"))
	require.Equal(t, []string{"alpha, remote_compaction_v2 ,beta", "remote_compaction_v2"}, header["x-codex-beta-features"], "the outbound request header must not be modified")

	onlyV2 := http.Header{}
	ensureOpenAIRemoteCompactionV2BetaFeature(onlyV2)
	require.Empty(t, withoutOpenAIRemoteCompactionV2Feature(onlyV2), "an API key compaction turn differs from its ordinary turn only by this token")
}

func TestOpenAIAgentRecoveryCompactionWireToleratesOnlyV2Negotiation(t *testing.T) {
	const url, model = "https://api.openai.com/v1/responses", "gpt-5.1"
	ordinary := http.Header{}
	ordinary.Set("Authorization", "Bearer fixture")
	owner := openAIAgentRecoveryWireDigest(url, ordinary, model)

	compaction := ordinary.Clone()
	ensureOpenAIRemoteCompactionV2BetaFeature(compaction)
	require.NotEqual(t, owner, openAIAgentRecoveryWireDigest(url, compaction, model))
	require.Equal(t, owner, openAIAgentRecoveryWireDigest(url, withoutOpenAIRemoteCompactionV2Feature(compaction), model))

	for name, mutate := range map[string]func(http.Header) (string, string){
		"credential": func(h http.Header) (string, string) { h.Set("Authorization", "Bearer other"); return url, model },
		"feature": func(h http.Header) (string, string) {
			h.Set("x-codex-beta-features", "other,remote_compaction_v2")
			return url, model
		},
		"url":   func(http.Header) (string, string) { return "https://other.example/v1/responses", model },
		"model": func(http.Header) (string, string) { return url, "gpt-5.1-mini" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := compaction.Clone()
			changedURL, changedModel := mutate(changed)
			require.NotEqual(t, owner, openAIAgentRecoveryWireDigest(changedURL, withoutOpenAIRemoteCompactionV2Feature(changed), changedModel))
		})
	}
}
