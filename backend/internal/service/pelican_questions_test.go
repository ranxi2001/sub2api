//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPelicanQuestionFrozenRounds(t *testing.T) {
	plan := &PelicanTestConfig{QuestionKind: "pelican", PromptMode: PelicanPromptModeRandom, ParallelCount: 3}
	for index := range pelicanQuestionBank {
		calls := 0
		round := freezePelicanQuestion(plan, "round", func(size int) int {
			calls++
			require.Equal(t, len(pelicanQuestionBank), size)
			return index
		})
		for range 3 {
			account := *round
			require.Equal(t, pelicanQuestionBank[index].id, account.PelicanQuestion.ID)
			require.Equal(t, round.ActualPrompt, intelligenceTestPrompt(&account))
		}
		require.Equal(t, 1, calls)
		require.Len(t, round.PelicanQuestion.Criteria, 4)
		require.Contains(t, round.ActualPrompt, PelicanDeliveryContract)
		body, err := json.Marshal(round)
		require.NoError(t, err)
		var saved PelicanTestConfig
		require.NoError(t, json.Unmarshal(body, &saved))
		require.Equal(t, round, &saved)
	}
	require.Empty(t, plan.Prompt)
	require.Nil(t, plan.PelicanQuestion)
	first := freezePelicanQuestion(plan, "one", func(int) int { return 0 })
	next := freezePelicanQuestion(plan, "two", func(int) int { return 1 })
	require.NotEqual(t, first.PelicanQuestion.ID, next.PelicanQuestion.ID)
}

func TestPelicanQuestionLegacyCustomAndManualIdentity(t *testing.T) {
	for _, mode := range []string{"", "custom"} {
		cfg := freezePelicanQuestion(&PelicanTestConfig{Prompt: "my custom SVG", PromptMode: mode}, "round", func(int) int { t.Fatal("custom must not select"); return 0 })
		require.Equal(t, "my custom SVG", cfg.Prompt)
		require.Nil(t, cfg.PelicanQuestion)
	}
	manual := freezePelicanQuestion(&PelicanTestConfig{PromptMode: PelicanPromptModeRandom}, "manual:00000000000000000000000000000000", nil)
	resolved, err := ResolveManualPelicanQuestion(manual.PelicanQuestion)
	require.NoError(t, err)
	require.Equal(t, manual.PelicanQuestion, resolved.PelicanQuestion)
	manual.PelicanQuestion.Version = "future"
	_, err = ResolveManualPelicanQuestion(manual.PelicanQuestion)
	require.Error(t, err)
}
