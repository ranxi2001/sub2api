// bps-image-cache stages relay images on the public edge before BPS fetches
// them. The only origin is the loopback SSH tunnel; it is not a URL proxy.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const (
	ttl        = 30 * time.Minute
	maxImage   = 20 << 20
	maxBytes   = 1 << 30
	maxEntries = 512
	imagePath  = "/api/bps-images/"
)

var validPath = regexp.MustCompile("^/api/bps-images/[A-Za-z0-9_-]{43}$")
var unavailable = errors.New("image not ready")

type entry struct {
	path, media string
	size        int64
	expires     time.Time
	readers     int
}

type flight struct {
	done chan struct{}
	err  error
}

type cache struct {
	mu                  sync.Mutex
	entries             map[string]*entry
	pending             map[string]*flight
	bytes               int64
	dir, origin         string
	client              *http.Client
	requests, downloads chan struct{}
}

func newCache(root, origin string) (*cache, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(root, "session-")
	if err != nil {
		return nil, err
	}
	c := &cache{dir: dir, origin: origin, entries: make(map[string]*entry), pending: make(map[string]*flight), requests: make(chan struct{}, 32), downloads: make(chan struct{}, 2)}
	c.client = &http.Client{Timeout: 140 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 2, IdleConnTimeout: time.Minute}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return c, nil
}

func (c *cache) pruneLocked(now time.Time) {
	for key, e := range c.entries {
		if !now.Before(e.expires) && e.readers == 0 {
			if err := os.Remove(e.path); err == nil || errors.Is(err, os.ErrNotExist) {
				delete(c.entries, key)
				c.bytes -= e.size
			}
		}
	}
}

func (c *cache) ensure(ctx context.Context, key string, warm bool) error {
	c.mu.Lock()
	now := time.Now()
	c.pruneLocked(now)
	if e := c.entries[key]; e != nil {
		if now.Before(e.expires) && (!warm || e.expires.Sub(now) > 150*time.Second) {
			c.mu.Unlock()
			return nil
		}
		if e.readers != 0 {
			c.mu.Unlock()
			return unavailable
		}
		if err := os.Remove(e.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			c.mu.Unlock()
			return unavailable
		}
		delete(c.entries, key)
		c.bytes -= e.size
	}
	if f := c.pending[key]; f != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return unavailable
		case <-f.done:
			return f.err
		}
	}
	if len(c.entries)+len(c.pending) >= maxEntries || c.bytes+int64(len(c.pending)+1)*maxImage > maxBytes {
		c.mu.Unlock()
		return unavailable
	}
	f := &flight{done: make(chan struct{})}
	c.pending[key] = f
	c.mu.Unlock()
	e, err := c.fetch(ctx, key)
	c.mu.Lock()
	if err == nil {
		c.entries[key] = e
		c.bytes += e.size
	}
	f.err = err
	delete(c.pending, key)
	close(f.done)
	c.mu.Unlock()
	return err
}

func (c *cache) fetch(ctx context.Context, key string) (*entry, error) {
	select {
	case c.downloads <- struct{}{}:
		defer func() { <-c.downloads }()
	case <-ctx.Done():
		return nil, unavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.origin+imagePath+key, nil)
	if err != nil {
		return nil, unavailable
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, unavailable
	}
	defer resp.Body.Close()
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	allowed := media == "image/png" || media == "image/jpeg" || media == "image/gif" || media == "image/webp"
	if err != nil || !allowed || resp.StatusCode != 200 || resp.ContentLength <= 0 || resp.ContentLength > maxImage {
		return nil, unavailable
	}
	f, err := os.CreateTemp(c.dir, "image-")
	if err != nil {
		return nil, unavailable
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(f.Name())
		}
	}()
	n, err := io.CopyBuffer(f, io.LimitReader(resp.Body, maxImage+1), make([]byte, 32*1024))
	if err != nil || n != resp.ContentLength {
		return nil, unavailable
	}
	if err := f.Close(); err != nil {
		return nil, unavailable
	}
	ok = true
	expires := time.Now().Add(ttl)
	if raw := resp.Header.Get("X-BPS-Image-Expires"); raw != "" {
		deadline, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || !time.Now().Before(time.Unix(deadline, 0)) {
			ok = false
			return nil, unavailable
		}
		if sourceExpiry := time.Unix(deadline, 0); sourceExpiry.Before(expires) {
			expires = sourceExpiry
		}
	}
	return &entry{path: f.Name(), size: n, media: media, expires: expires}, nil
}

func (c *cache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.URL.Path == "/health" && r.Method == http.MethodGet {
		w.WriteHeader(200)
		return
	}
	if !validPath.MatchString(r.URL.Path) || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(405)
		return
	}
	select {
	case c.requests <- struct{}{}:
		defer func() { <-c.requests }()
	default:
		w.WriteHeader(503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 145*time.Second)
	defer cancel()
	key := r.URL.Path[len(imagePath):]
	if err := c.ensure(ctx, key, r.Header.Get("X-BPS-Image-Prewarm") == "1"); err != nil {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(503)
		return
	}
	c.mu.Lock()
	e := c.entries[key]
	if e == nil || !time.Now().Before(e.expires) {
		c.mu.Unlock()
		w.WriteHeader(503)
		return
	}
	f, err := os.Open(e.path)
	if err != nil {
		c.mu.Unlock()
		w.WriteHeader(503)
		return
	}
	e.readers++
	c.mu.Unlock()
	defer func() { f.Close(); c.mu.Lock(); e.readers--; c.mu.Unlock() }()
	w.Header().Set("Content-Type", e.media)
	w.Header().Set("Content-Length", strconv.FormatInt(e.size, 10))
	w.Header().Set("X-BPS-Image-Ready", "1")
	w.WriteHeader(200)
	if r.Method == http.MethodGet {
		_, _ = io.Copy(w, f)
	}
}

func main() {
	root := flag.String("dir", "/var/lib/bps-image-cache", "private cache directory")
	flag.Parse()
	c, err := newCache(*root, "http://127.0.0.1:28081")
	if err != nil {
		log.Fatal("image cache initialization failed")
	}
	defer os.RemoveAll(c.dir)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: "127.0.0.1:28082", Handler: c, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 150 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 8192}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				c.mu.Lock()
				c.pruneLocked(now)
				c.mu.Unlock()
				os.Chtimes(c.dir, now, now)
				// Delete only abandoned directories owned by this service after TTL.
				dirs, _ := filepath.Glob(filepath.Join(*root, "session-*"))
				for _, path := range dirs {
					if path != c.dir {
						info, err := os.Stat(path)
						if err == nil && info.IsDir() && now.Sub(info.ModTime()) > ttl+time.Minute {
							os.RemoveAll(path)
						}
					}
				}
			}
		}
	}()
	fmt.Println("BPS image cache listening on loopback; bounded disk storage enabled")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal("image cache server failed")
	}
}
