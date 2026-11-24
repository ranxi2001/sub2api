package server

import (
	"errors"
	"io/fs"
	"os"
	"regexp"
)

// Match only files created by the former recorder in its per-node spool.
// Keep the directory itself because it can be a dedicated mount point.
var legacyTrafficFilename = regexp.MustCompile(`^(?:[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\.json(?:\.invalid)?|\.traffic-[0-9]+)$`)

func removeLegacyTrafficFiles(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() && legacyTrafficFilename.MatchString(entry.Name()) {
			if err := root.Remove(entry.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}
