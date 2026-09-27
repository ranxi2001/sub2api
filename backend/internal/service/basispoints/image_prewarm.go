package basispoints

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

var ErrImageRelayPrewarm = errors.New("basispoints images could not be prepared at the public image server; retry shortly")

var imagePrewarmClient = &http.Client{
	Transport:     &http.Transport{MaxIdleConns: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: time.Minute, TLSHandshakeTimeout: 10 * time.Second},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Prewarm waits for complete images at the public edge before BPS starts its
// short download timeout. Only this relay's existing capability URLs qualify;
// arbitrary user-supplied URLs and credentials never enter this client.
func (r *ImageRelay) Prewarm(ctx context.Context, raw []byte) error {
	return r.prewarm(ctx, raw, imagePrewarmClient)
}

func (r *ImageRelay) prewarm(ctx context.Context, raw []byte, client *http.Client) error {
	if r == nil {
		return nil
	}
	var source object
	if err := decode(raw, &source); err != nil {
		return ErrImageRelayPrewarm
	}
	r.mu.Lock()
	base := r.baseURL + ImageRelayPath
	images := make(map[string]int)
	input, _ := source["input"].([]any)
	for _, rawItem := range input {
		item, _ := rawItem.(object)
		for _, field := range []string{"content", "output"} {
			if field == "output" && text(item["type"]) != "function_call_output" && text(item["type"]) != "custom_tool_call_output" {
				continue
			}
			parts, _ := item[field].([]any)
			for _, rawPart := range parts {
				part, _ := rawPart.(object)
				if text(part["type"]) != "input_image" {
					continue
				}
				u := text(part["image_url"])
				if token, ok := strings.CutPrefix(u, base); ok {
					if img := r.entries[token]; img != nil && time.Now().Before(img.expires) {
						images[u] = img.size
					}
				}
			}
		}
	}
	r.mu.Unlock()
	if len(images) == 0 {
		return nil
	}
	// These are existing relay entries, already bounded at admission. Lowering
	// limits must not invalidate an in-flight batch.
	ctx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(2)
	for imageURL, size := range images {
		group.Go(func() error {
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, imageURL, nil)
			if err != nil {
				return ErrImageRelayPrewarm
			}
			req.Header.Set("X-BPS-Image-Prewarm", "1")
			resp, err := client.Do(req)
			if err != nil {
				return ErrImageRelayPrewarm
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK || resp.ContentLength != int64(size) || resp.Header.Get("X-BPS-Image-Ready") != "1" {
				return ErrImageRelayPrewarm
			}
			return nil
		})
	}
	return group.Wait()
}
