package reauthruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func archive(t *testing.T, entries []*tar.Header, content []string) []byte {
	t.Helper()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	for i, h := range entries {
		require.NoError(t, tw.WriteHeader(h))
		if h.Typeflag == tar.TypeReg {
			_, err := tw.Write([]byte(content[i]))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return data.Bytes()
}

func TestExtractRejectsEscapesAndLinks(t *testing.T) {
	for _, h := range []*tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg, Size: 1},
		{Name: "/escape", Typeflag: tar.TypeReg, Size: 1},
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../escape"},
		{Name: "device", Typeflag: tar.TypeChar},
	} {
		t.Run(h.Name, func(t *testing.T) {
			require.Error(t, extract(archive(t, []*tar.Header{h}, []string{"x"}), t.TempDir()))
		})
	}
}

func TestExtractPreservesExecutableAndRejectsDuplicate(t *testing.T) {
	root := t.TempDir()
	h := &tar.Header{Name: "bin/run", Typeflag: tar.TypeReg, Size: 4, Mode: 0755}
	require.NoError(t, extract(archive(t, []*tar.Header{h}, []string{"test"}), root))
	info, err := os.Stat(filepath.Join(root, "bin/run"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	require.Error(t, extract(archive(t, []*tar.Header{h}, []string{"test"}), root))
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPrepareVerifiesDigestBeforeExecutingAndReusesCache(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux runtime")
	}
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{true: "valid", false: "tampered"}[valid], func(t *testing.T) {
			root := t.TempDir()
			script := "#!/bin/sh\nexit 0\n"
			data := archive(t, []*tar.Header{{Name: "python", Typeflag: tar.TypeReg, Size: int64(len(script)), Mode: 0755}}, []string{script})
			sum := sha256.Sum256(data)
			digest := hex.EncodeToString(sum[:])
			if !valid {
				digest = strings.Repeat("0", 64)
			}
			requests := 0
			m := New(root, "1.2.3", "http://127.0.0.1:4040", strings.Repeat("x", 64))
			m.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				require.NotContains(t, req.URL.String(), m.token)
				body := data
				if strings.Contains(req.URL.Path, "/releases/tags/") {
					body, _ = json.Marshal(map[string]any{"assets": []map[string]string{{"name": "sub2api-reauth_1.2.3_linux_" + runtime.GOARCH + ".tar.gz", "digest": "sha256:" + digest}}})
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
			})}
			dir, err := m.prepare(context.Background())
			if !valid {
				require.ErrorContains(t, err, "checksum")
				return
			}
			require.NoError(t, err)
			require.FileExists(t, filepath.Join(dir, "ready"))
			_, err = m.prepare(context.Background())
			require.NoError(t, err)
			require.Equal(t, 2, requests)
		})
	}
}

func TestStopCancelsPreparationAndCannotRestart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux runtime preparation")
	}
	m := New(t.TempDir(), "1.2.3", "http://127.0.0.1:4040", strings.Repeat("x", 64))
	entered := make(chan struct{})
	m.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		close(entered)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	m.Ensure()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("preparation did not start")
	}
	m.Stop()
	require.Equal(t, "unavailable", m.Status().State)
	m.Ensure()
	require.False(t, m.started)
}

func TestManagedProcessGetsOnlyWorkerEnvironmentAndStops(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux process supervision")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "1.2.3-linux-"+runtime.GOARCH)
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ready"), []byte("cached"), 0600))
	script := "#!/bin/sh\nprintf '%s' \"$OPENAI_REAUTH_WORKER_TOKEN\" > worker-token\nprintf '%s' \"$DATABASE_PASSWORD\" > unrelated-secret\nprintf '%s' \"$OPENAI_REAUTH_CONCURRENCY\" > concurrency\nexec /bin/sleep 60\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "python"), []byte(script), 0700))
	t.Setenv("OPENAI_REAUTH_CONCURRENCY", "4")
	t.Setenv("DATABASE_PASSWORD", "must-not-inherit")
	m := New(root, "1.2.3", "http://127.0.0.1:4040", "synthetic-worker-token")
	m.Ensure()
	defer m.Stop()
	// Child output can precede the parent's running status update after Start.
	// Wait for both independently, including the last environment file write.
	require.Eventually(t, func() bool {
		return m.Status().State == "running"
	}, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		got, err := os.ReadFile(filepath.Join(dir, "concurrency"))
		return err == nil && string(got) == "4"
	}, 5*time.Second, 10*time.Millisecond)
	got, err := os.ReadFile(filepath.Join(dir, "worker-token"))
	require.NoError(t, err)
	require.Equal(t, "synthetic-worker-token", string(got))
	got, err = os.ReadFile(filepath.Join(dir, "unrelated-secret"))
	require.NoError(t, err)
	require.Empty(t, got)
	got, err = os.ReadFile(filepath.Join(dir, "concurrency"))
	require.NoError(t, err)
	require.Equal(t, "4", string(got))
	m.Stop()
	require.Equal(t, "stopped", m.Status().State)
}
