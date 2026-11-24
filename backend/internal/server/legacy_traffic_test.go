package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemoveLegacyTrafficFiles(t *testing.T) {
	dir := t.TempDir()
	const event = "a67a80b1-d32c-46f8-b81d-a317a678de47.json"
	for _, name := range []string{event, event + ".invalid", ".traffic-1234", "keep.json"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600))
	}
	link := filepath.Join(t.TempDir(), "user-traffic-pending")
	require.NoError(t, os.Symlink(dir, link))
	require.NoError(t, removeLegacyTrafficFiles(link))
	require.FileExists(t, filepath.Join(dir, event))
	require.NoError(t, removeLegacyTrafficFiles(dir))
	require.NoError(t, removeLegacyTrafficFiles(dir))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "keep.json", entries[0].Name())
}
