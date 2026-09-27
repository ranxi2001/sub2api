// Command bps-image-compat-probe performs one bounded, opt-in deployment check.
// Credentials arrive on stdin, remain in memory, and are never printed. It
// starts no server and changes no application settings or database records.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
)

type input struct {
	Mode      string `json:"mode"`
	Token     string `json:"token"`
	AccountID string `json:"account_id"`
	ProxyURL  string `json:"proxy_url"`
	APIKey    string `json:"api_key"`
	OutputDir string `json:"output_dir"`
}

func terminal(raw []byte) (map[string]any, error) {
	var response map[string]any
	scan := bufio.NewScanner(bytes.NewReader(raw))
	scan.Buffer(make([]byte, 4096), 16<<20)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event map[string]any
		decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(strings.TrimPrefix(line, "data:"))))
		decoder.UseNumber()
		if decoder.Decode(&event) != nil {
			continue
		}
		kind, _ := event["type"].(string)
		if kind == "response.completed" || kind == "response.failed" || kind == "response.incomplete" {
			response, _ = event["response"].(map[string]any)
		}
	}
	if scan.Err() != nil || response == nil || response["status"] != "completed" {
		return response, errors.New("response did not complete")
	}
	return response, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Println("PROBE_FAILED: " + err.Error())
		os.Exit(1)
	}
}

func run() error {
	var cfg input
	if json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&cfg) != nil || cfg.Token == "" || cfg.AccountID == "" || cfg.APIKey == "" || cfg.OutputDir == "" {
		return errors.New("missing probe input")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.ProxyURL != "" {
		proxy, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return errors.New("invalid proxy")
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	bpsClient := &http.Client{Transport: transport, Timeout: 4 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	localClient := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 4 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	results := []map[string]any{}
	modes := []string{"text", "image"}
	if cfg.Mode == "history" {
		modes = []string{"history"}
	} else if cfg.Mode != "" {
		return errors.New("unsupported probe mode")
	}
	for _, mode := range modes {
		started := time.Now()
		calls := 0
		prompt := "Reply with exactly OK."
		if mode == "image" {
			prompt = "请直接生成一张小猫喝水的照片，柔和自然光，清晰可爱。不要只给提示词。"
		}
		source, _ := json.Marshal(map[string]any{"model": "gpt-6-astra", "input": prompt, "stream": true, "reasoning": map[string]any{"effort": "low"}, "instructions": "Help the user complete their request."})
		if mode == "history" {
			delegation := map[string]any{"type": "function_call_output", "id": "fco_probe", "name": "send_message_to_thread", "namespace": "codex_app", "output": "<codex_delegation><source_thread_id>probe</source_thread_id><input>Generate a dog drinking water</input></codex_delegation>"}
			source, _ = json.Marshal(map[string]any{"model": "gpt-6-astra", "stream": true, "tool_choice": "none", "reasoning": map[string]any{"effort": "low"}, "input": []any{
				delegation,
				map[string]any{"type": "image_generation_call", "id": "ig_bps_probe_history", "status": "completed", "result": "omitted"},
				delegation,
				map[string]any{"type": "message", "role": "user", "content": "Reply with exactly HISTORY_OK. Do not generate an image."},
			}})
		}
		var nativeHeaders map[string]string
		wire, bridge, err := basispoints.PrepareWithImageGeneration(source, "image-compat-probe-"+mode+time.Now().Format(time.RFC3339Nano), nil, func(request basispoints.ImageGenerationRequest) (basispoints.ImageGenerationResult, error) {
			calls++
			if calls > 1 {
				return basispoints.ImageGenerationResult{}, errors.New("unexpected repeated generation")
			}
			body, _ := json.Marshal(map[string]any{"model": "gpt-6-astra", "stream": true, "store": false, "instructions": "Generate exactly one image using the image_generation tool.", "input": request.Prompt, "tools": []any{map[string]any{"type": "image_generation", "size": request.Size, "quality": request.Quality, "output_format": "png"}}, "tool_choice": map[string]any{"type": "image_generation"}})
			req, _ := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:28080/v1/responses", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "codex_cli_rs/0.98.0")
			resp, err := localClient.Do(req)
			if err != nil {
				return basispoints.ImageGenerationResult{}, errors.New("native connection failed")
			}
			defer resp.Body.Close()
			nativeHeaders = map[string]string{"upstream": resp.Header.Get("X-Codex2API-Upstream"), "bypass": resp.Header.Get("X-Codex2API-Basispoints-Bypass")}
			if resp.StatusCode != 200 {
				return basispoints.ImageGenerationResult{}, fmt.Errorf("native HTTP %d", resp.StatusCode)
			}
			raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
			if err != nil {
				return basispoints.ImageGenerationResult{}, errors.New("native read failed")
			}
			completed, err := terminal(raw)
			if err != nil {
				return basispoints.ImageGenerationResult{}, err
			}
			result := basispoints.ImageGenerationResult{}
			result.Usage, _ = completed["usage"].(map[string]any)
			result.ToolUsage, _ = completed["tool_usage"].(map[string]any)
			output, _ := completed["output"].([]any)
			for _, raw := range output {
				item, _ := raw.(map[string]any)
				if item["type"] == "image_generation_call" {
					result.Item = item
				}
			}
			return result, nil
		})
		if err != nil {
			return errors.New("prepare failed")
		}
		req, _ := http.NewRequestWithContext(ctx, "POST", basispoints.ResponsesURL, bytes.NewReader(wire))
		req.Header = http.Header{"Authorization": {"Bearer " + cfg.Token}, "Chatgpt-Account-Id": {cfg.AccountID}, "X-Openai-Account-Id": {cfg.AccountID}, "X-Basispoints-Auth-Mode": {"chatgpt"}, "Content-Type": {"application/json"}, "Accept": {"text/event-stream"}, "Origin": {"https://bps.openai.com"}, "User-Agent": {"Mozilla/5.0"}, "X-Openai-Internal-Basispoints-Client-Product": {"basispoints-excel-plugin"}, "X-Openai-Internal-Basispoints-Client-Agent-Profile": {"excel"}}
		resp, err := bpsClient.Do(req)
		if err != nil {
			return errors.New("BPS connection failed")
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return fmt.Errorf("BPS HTTP %d", resp.StatusCode)
		}
		converted := bridge.Stream(resp.Body)
		raw, err := io.ReadAll(io.LimitReader(converted, 32<<20))
		converted.Close()
		if err != nil {
			return errors.New("bridge stream read failed")
		}
		completed, err := terminal(raw)
		if err != nil {
			return fmt.Errorf("%s bridge response did not complete; image_calls=%d", mode, calls)
		}
		row := map[string]any{"mode": mode, "image_calls": calls, "seconds": time.Since(started).Seconds(), "status": completed["status"], "usage": completed["usage"], "native_headers": nativeHeaders}
		if mode == "history" {
			answer := ""
			for _, raw := range completed["output"].([]any) {
				item, _ := raw.(map[string]any)
				parts, _ := item["content"].([]any)
				for _, rawPart := range parts {
					part, _ := rawPart.(map[string]any)
					if part["type"] == "output_text" {
						text, _ := part["text"].(string)
						answer += text
					}
				}
			}
			if calls != 0 || strings.TrimSpace(answer) != "HISTORY_OK" {
				return errors.New("history continuation check failed")
			}
			row["answer"] = "HISTORY_OK"
		}
		if mode == "text" && calls != 0 {
			return errors.New("ordinary text triggered image generation")
		}
		if mode == "image" {
			if calls != 1 {
				return errors.New("BPS model did not select the image tool")
			}
			var imageBytes []byte
			output, _ := completed["output"].([]any)
			for _, raw := range output {
				item, _ := raw.(map[string]any)
				if item["type"] == "image_generation_call" {
					result, _ := item["result"].(string)
					imageBytes, err = base64.StdEncoding.DecodeString(result)
				}
			}
			if err != nil || len(imageBytes) == 0 {
				return errors.New("invalid generated image")
			}
			info, format, err := image.DecodeConfig(bytes.NewReader(imageBytes))
			if err != nil || format != "png" {
				return errors.New("generated PNG validation failed")
			}
			if err = os.WriteFile(filepath.Join(cfg.OutputDir, "compat-kitten.png"), imageBytes, 0600); err != nil {
				return errors.New("cannot save generated PNG")
			}
			hash := sha256.Sum256(imageBytes)
			row["sha256"] = hex.EncodeToString(hash[:])
			row["bytes"] = len(imageBytes)
			row["width"], row["height"] = info.Width, info.Height
		}
		results = append(results, row)
		safe, _ := json.Marshal(row)
		fmt.Println("PROBE_RESULT " + string(safe))
	}
	raw, _ := json.MarshalIndent(results, "", "  ")
	if os.WriteFile(filepath.Join(cfg.OutputDir, "live-results.json"), raw, 0600) != nil {
		return errors.New("cannot save probe summary")
	}
	fmt.Println("PROBE_COMPLETE")
	return nil
}
