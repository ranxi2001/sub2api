package repository

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func useGroupUsageRepositoryTestTimezone(t *testing.T, name string) configuredTimezone {
	t.Helper()
	loc, err := time.LoadLocation(name)
	require.NoError(t, err)
	return configuredTimezone{location: loc}
}
