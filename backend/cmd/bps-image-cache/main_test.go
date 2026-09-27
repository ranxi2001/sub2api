package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrewarmWaitsForFullBodyThenServesWithoutOrigin(t *testing.T) {
	data := strings.Repeat("p", 384000)
	release := make(chan struct{})
	started := make(chan struct{})
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		<-release
		io.WriteString(w, data)
	}))
	c, err := newCache(t.TempDir(), origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	path := imagePath + strings.Repeat("a", 43)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest("HEAD", path, nil)
		req.Header.Set("X-BPS-Image-Prewarm", "1")
		w := httptest.NewRecorder()
		c.ServeHTTP(w, req)
		done <- w
	}()
	<-started
	select {
	case <-done:
		t.Fatal("ready before body complete")
	default:
	}
	close(release)
	w := <-done
	if w.Code != 200 || w.Header().Get("X-BPS-Image-Ready") != "1" || w.Body.Len() != 0 {
		t.Fatal("prewarm failed")
	}
	origin.Close()
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			c.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			if w.Code != 200 || w.Body.String() != data {
				t.Error("cached body incomplete")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("unexpected repeated origin fetch")
	}
	c.mu.Lock()
	c.entries[strings.Repeat("a", 43)].expires = time.Now().Add(-time.Second)
	c.pruneLocked(time.Now())
	bytes := c.bytes
	n := len(c.entries)
	c.mu.Unlock()
	if n != 0 || bytes != 0 {
		t.Fatal("expired cache retained")
	}
}

func TestIncompleteAndInvalidImagesAreNeverPublished(t *testing.T) {
	for _, tc := range []struct{ name, media, body, length string }{{"truncated", "image/png", "abc", "999"}, {"oversized", "image/png", "", strconv.Itoa(maxImage + 1)}, {"html", "text/html", "abc", "3"}} {
		t.Run(tc.name, func(t *testing.T) {
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.media)
				w.Header().Set("Content-Length", tc.length)
				io.WriteString(w, tc.body)
			}))
			defer origin.Close()
			c, err := newCache(t.TempDir(), origin.URL)
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			c.ServeHTTP(w, httptest.NewRequest("HEAD", imagePath+strings.Repeat("b", 43), nil))
			files, _ := os.ReadDir(c.dir)
			if w.Code != 503 || len(c.entries) != 0 || len(files) != 0 || w.Header().Get("X-BPS-Image-Ready") != "" {
				t.Fatal("invalid body retained")
			}
		})
	}
}

func TestCacheRejectsNonImagePathsAndCoalescesFetches(t *testing.T) {
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(40 * time.Millisecond)
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", "3")
		io.WriteString(w, "abc")
	}))
	defer origin.Close()
	c, err := newCache(t.TempDir(), origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/admin", "/api/bps-images/../secret", imagePath + strings.Repeat("a", 43) + "?url=http://example.com"} {
		w := httptest.NewRecorder()
		c.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatal("unexpected path accepted")
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			c.ServeHTTP(w, httptest.NewRequest("HEAD", imagePath+strings.Repeat("c", 43), nil))
			if w.Code != 200 {
				t.Error("coalesced request failed")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("download not coalesced")
	}
}
