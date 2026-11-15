package qqbot

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithConfigVersionNormalizesCompressedETags(t *testing.T) {
	config := defaultRankingConfig()
	version := ConfigVersion(config)

	for _, etag := range []string{`"` + version + `-gzip"`, `"` + version + `-zstd"`, `W/"` + version + `-gzip"`} {
		t.Run(etag, func(t *testing.T) {
			err := checkConfigVersion(WithConfigVersion(context.Background(), etag), config)
			require.NoError(t, err)
		})
	}

	err := checkConfigVersion(WithConfigVersion(context.Background(), `"stale-gzip"`), config)
	require.ErrorIs(t, err, ErrConfigConflict)
}
