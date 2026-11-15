package qqbot

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHealthSustainedFailureRecoveryAndCooldown(t *testing.T) {
	c := DefaultHealthNotificationConfig()
	c.FailureMinutes = 1
	c.RecoveryMinutes = 1
	at := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	ttft := int64(1000)
	m := HealthMetric{Requests: 100, ErrorRate: .3, TTFTMs: &ttft}
	state, event := advanceHealth(healthState{}, m, c, at, true)
	require.Empty(t, event)
	state, event = advanceHealth(state, m, c, at.Add(time.Minute), true)
	require.Equal(t, "failure", event)
	state, event = advanceHealth(state, m, c, at.Add(2*time.Minute), true)
	require.Empty(t, event)
	m.ErrorRate = .1
	state, event = advanceHealth(state, m, c, at.Add(3*time.Minute), true)
	require.Empty(t, event)
	require.True(t, state.Active)
	m.ErrorRate = .01
	state, event = advanceHealth(state, m, c, at.Add(4*time.Minute), true)
	require.Empty(t, event)
	state, event = advanceHealth(state, m, c, at.Add(5*time.Minute), true)
	require.Equal(t, "recovery", event)
	m.ErrorRate = .3
	state, event = advanceHealth(state, m, c, at.Add(6*time.Minute), true)
	require.Empty(t, event)
	_, event = advanceHealth(state, m, c, at.Add(7*time.Minute), true)
	require.Empty(t, event, "cooldown suppresses a new incident alert")
}

func TestHealthMissingSamplesAndGapsNeverRecover(t *testing.T) {
	c := DefaultHealthNotificationConfig()
	c.RecoveryMinutes = 1
	c.FailureMinutes = 1
	at := time.Now()
	ttft := int64(1000)
	m := HealthMetric{Requests: 100, ErrorRate: 0, TTFTMs: &ttft}
	for _, invalid := range []string{"samples", "stale", "latency"} {
		t.Run(invalid, func(t *testing.T) {
			state, _ := advanceHealth(healthState{Active: true}, m, c, at, true)
			x := m
			valid := true
			switch invalid {
			case "samples":
				x.Requests = 0
			case "stale":
				valid = false
			case "latency":
				x.TTFTMs = nil
			}
			state, event := advanceHealth(state, x, c, at.Add(time.Minute), valid)
			require.Empty(t, event)
			require.True(t, state.Active)
		})
	}
	m.ErrorRate = 1
	state, _ := advanceHealth(healthState{}, m, c, at, true)
	_, event := advanceHealth(state, m, c, at.Add(5*time.Minute), true)
	require.Empty(t, event, "observation gap resets confirmation")
}
