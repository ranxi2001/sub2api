package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type aiOpsAdmissionRepo struct {
	turnAdmissionRepo
	paused bool
	errAI  error
}

func (r *aiOpsAdmissionRepo) AIOpsRuntime(context.Context, []int64) (map[int64]AIOpsRuntime, error) {
	if r.errAI != nil {
		return nil, r.errAI
	}
	return map[int64]AIOpsRuntime{r.account.ID: {Paused: r.paused, Concurrency: r.account.Concurrency}}, nil
}

func TestAIOpsPauseIsCheckedAfterAdmissionCacheSelection(t *testing.T) {
	a := ticketTestAccount(901)
	r := &aiOpsAdmissionRepo{turnAdmissionRepo: turnAdmissionRepo{account: a}}
	s := &OpenAIGatewayService{accountRepo: r}
	_, err := s.latestOpenAITurnAccountForGroup(context.Background(), a, 0, false)
	require.NoError(t, err)
	r.paused = true
	_, err = s.latestOpenAITurnAccountForGroup(context.Background(), a, 0, false)
	require.Error(t, err)
	r.paused = false
	r.errAI = errors.New("unavailable")
	_, err = s.latestOpenAITurnAccountForGroup(context.Background(), a, 0, false)
	require.Error(t, err)
}

func TestAIOpsHostGateRequiresExplicitFullDeployment(t *testing.T) {
	s := &OpsService{}
	t.Setenv("SUB2API_AI_OPS_CONTROL_ENABLED", "false")
	require.False(t, s.AIOpsControlEnabled())
	t.Setenv("SUB2API_AI_OPS_CONTROL_ENABLED", "true")
	require.True(t, s.AIOpsControlEnabled())
	s.cfg = &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleGateway}}
	require.False(t, s.AIOpsControlEnabled())
	s.cfg.Runtime.Role = config.RuntimeRoleFull
	s.cfg.Runtime.RegionalSchedulerFile = "/isolated/snapshot"
	require.False(t, s.AIOpsControlEnabled())
}
