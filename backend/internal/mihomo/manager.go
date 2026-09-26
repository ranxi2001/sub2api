// Package mihomo manages a private, unprivileged ticket-harvest proxy.
package mihomo

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const Version = "v1.19.31"
const Endpoint = "http://127.0.0.1:3101"

var managers sync.Map

// CloseAll is called after HTTP shutdown so no new management work can enter.
func CloseAll() {
	managers.Range(func(key, value any) bool {
		if m, ok := key.(*Manager); ok {
			m.Close()
		}
		managers.Delete(key)
		return true
	})
}

type Status struct {
	BPSWarmPool      BPSWarmStatus `json:"bps_warm_pool"`
	BPSIPWarmPool    BPSWarmStatus `json:"bps_ip_warm_pool"`
	CountryFilter    CountryFilter `json:"country_filter"`
	CountryCodes     []string      `json:"country_codes"`
	EligibleNodes    int           `json:"eligible_nodes"`
	CountryExcluded  int           `json:"country_excluded"`
	UnknownCountries int           `json:"unknown_countries"`
	UseOnce          bool          `json:"use_once"`
	Installed        bool          `json:"installed"`
	Running          bool          `json:"running"`
	Busy             bool          `json:"busy"`
	Phase            string        `json:"phase"`
	Error            string        `json:"error,omitempty"`
	Subscriptions    int           `json:"subscriptions"`
	DynamicProxies   int           `json:"dynamic_proxies"`
	Nodes            int           `json:"nodes"`
	Endpoint         string        `json:"endpoint"`
	Supported        bool          `json:"supported"`
	NodeStates       []NodeStatus  `json:"node_states"`
}

type NodeStatus struct {
	Dynamic          bool       `json:"dynamic"`
	CountryCode      string     `json:"country_code,omitempty"`
	CountryCheckedAt *time.Time `json:"country_checked_at,omitempty"`
	CountryError     string     `json:"country_error,omitempty"`
	CountryBlocked   bool       `json:"country_blocked"`
	Name             string     `json:"name"`
	DisplayName      string     `json:"display_name,omitempty"`
	State            string     `json:"state"`
}

type saved struct {
	CountryFilter  CountryFilter                 `json:"country_filter"`
	Countries      map[string]CountryObservation `json:"countries,omitempty"`
	UseOnce        bool                          `json:"use_once,omitempty"`
	URLs           []string                      `json:"urls"`
	DynamicProxies []string                      `json:"dynamic_proxies,omitempty"`
	Nodes          []map[string]any              `json:"nodes"`
	NodeNames      map[string]string             `json:"node_names,omitempty"`
	Secret         string                        `json:"secret"`
	Disabled       map[string]string             `json:"disabled,omitempty"`
}

type Manager struct {
	bpsWarmMu     sync.Mutex
	bpsWarmTarget int
	bpsMu         sync.Mutex
	bpsPorts      map[string]int
	bpsSessions   map[string]*bpsSession
	bpsHealth     map[string]*bpsNodeHealth
	bpsDynamic    map[string]bool                     // configured dynamic outbound identities; protected by bpsMu
	bpsProbe      func(context.Context, string) error // isolated tests only
	bpsStaticMode bool                                // dedicated static (IP 管理) pool: no kernel, no listeners
	bpsStatic     map[string]string                   // static node key -> upstream proxy URL; protected by bpsMu

	countryLookupURL     string // test-only override; administrators cannot change the lookup target
	controllerURL        string // optional override for isolated controller tests
	subscriptionProxyURL string // test-only override for subscription download proxy
	gate                 chan struct{}
	mu                   sync.Mutex
	dir                  string
	state                Status
	saved                saved
	cmd                  *exec.Cmd
	done                 chan struct{}
	cancel               context.CancelFunc
	closed               bool
	wg                   sync.WaitGroup
	client               *http.Client
}

func New(dir string) *Manager {
	m := &Manager{dir: dir, client: &http.Client{Timeout: 30 * time.Second}, gate: make(chan struct{}, 1)}
	m.state = Status{Endpoint: Endpoint, Phase: "not_installed", Supported: runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64")}
	if b, err := os.ReadFile(filepath.Join(dir, "settings.json")); err == nil {
		_ = json.Unmarshal(b, &m.saved)
	}
	if fi, err := os.Stat(filepath.Join(dir, "mihomo")); err == nil && fi.Mode().Perm()&0111 != 0 {
		m.state.Installed = true
		m.state.Phase = "ready"
	}
	managers.Store(m, struct{}{})
	if m.state.Installed && len(m.saved.Nodes) > 0 {
		_ = m.Submit("start", nil, false)
	}
	return m
}

func (m *Manager) Status() Status {
	s := m.baseStatus()
	s.BPSWarmPool = m.BPSWarmStatus()
	s.BPSIPWarmPool = bpsStaticManager.BPSWarmStatus()
	return s
}
func (m *Manager) baseStatus() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.state
	s.CountryFilter = m.saved.CountryFilter
	if s.CountryFilter.Mode == "" {
		s.CountryFilter.Mode = "off"
	}
	s.CountryFilter.Codes = append([]string{}, s.CountryFilter.Codes...)
	s.CountryCodes = countryCodes()
	s.UseOnce = m.saved.UseOnce
	s.Subscriptions = len(m.saved.URLs)
	s.DynamicProxies = len(m.saved.DynamicProxies)
	s.Nodes = len(m.saved.Nodes)
	for _, n := range m.saved.Nodes {
		if name, ok := n["name"].(string); ok {
			state := "enabled"
			if disabled := m.saved.Disabled[name]; disabled != "" {
				state = disabled
			}
			observation := m.saved.Countries[name]
			dynamic := strings.HasPrefix(name, "DYNAMIC-")
			if dynamic {
				observation = CountryObservation{}
			}
			blocked := !countryAllowed(m.saved, name)
			if !validCountry(observation.Code) {
				s.UnknownCountries++
			}
			if blocked {
				s.CountryExcluded++
				if state == "enabled" {
					state = "country_excluded"
				}
			} else if state == "enabled" {
				s.EligibleNodes++
			}
			node := NodeStatus{Dynamic: dynamic, Name: name, DisplayName: m.saved.NodeNames[name], State: state, CountryCode: observation.Code, CountryError: observation.Error, CountryBlocked: blocked}
			if !observation.CheckedAt.IsZero() {
				checked := observation.CheckedAt
				node.CountryCheckedAt = &checked
			}
			s.NodeStates = append(s.NodeStates, node)
		}
	}
	return s
}

// Submit serializes long-running work and never returns subprocess output or URLs.
func (m *Manager) Submit(action string, urls []string, appendURLs bool, filters ...*CountryFilter) error {
	return m.SubmitWithDynamicProxies(action, urls, nil, appendURLs, filters...)
}

// SubmitWithDynamicProxies accepts raw proxy URLs in addition to remote
// Clash/Mihomo subscriptions. The legacy Submit method remains unchanged for
// callers that do not use the dynamic source.
func (m *Manager) SubmitWithDynamicProxies(action string, urls, dynamicProxies []string, appendURLs bool, filters ...*CountryFilter) error {
	op, node, hasNode := strings.Cut(action, "/")
	if op != "install" && op != "apply" && op != "apply_dynamic" && op != "start" && op != "disable" && op != "recover" && op != "probe" && op != "once_on" && op != "once_off" && op != "country_filter" && op != "country_scan" && op != "country_probe" {
		return errors.New("unknown operation")
	}
	if (op == "disable" || op == "recover" || op == "probe" || op == "country_probe") != hasNode || (hasNode && (node == "" || strings.Contains(node, "/"))) {
		return errors.New("invalid operation target")
	}
	clean, err := normalizeURLs(urls)
	if err != nil {
		return err
	}
	cleanDynamic, err := normalizeDynamicProxies(dynamicProxies)
	if err != nil {
		return err
	}
	var filter CountryFilter
	if op == "country_filter" {
		if len(filters) != 1 || filters[0] == nil {
			return errors.New("country filter is required")
		}
		filter, err = normalizeCountryFilter(*filters[0])
		if err != nil {
			return err
		}
	}
	m.mu.Lock()
	if m.closed || m.state.Busy {
		m.mu.Unlock()
		return errors.New("another operation is running or service is stopping")
	}
	if !m.state.Supported {
		m.mu.Unlock()
		return errors.New("requires Linux amd64 or arm64")
	}
	next := m.saved
	if op == "country_filter" {
		next.CountryFilter = filter
	}
	if op == "apply_dynamic" {
		if len(cleanDynamic) == 0 {
			m.mu.Unlock()
			return errors.New("a dynamic proxy is required")
		}
		next.URLs = nil
		next.DynamicProxies = cleanDynamic
	}
	if action == "apply" && len(clean) > 0 {
		if appendURLs {
			merged, mergeErr := normalizeURLs(append(append([]string{}, next.URLs...), clean...))
			if mergeErr != nil {
				m.mu.Unlock()
				return mergeErr
			}
			next.URLs = merged
		} else {
			next.URLs = clean
		}
	}
	if action == "apply" && len(cleanDynamic) > 0 {
		if appendURLs {
			merged, mergeErr := normalizeDynamicProxies(append(append([]string{}, next.DynamicProxies...), cleanDynamic...))
			if mergeErr != nil {
				m.mu.Unlock()
				return mergeErr
			}
			next.DynamicProxies = merged
		} else {
			next.DynamicProxies = cleanDynamic
		}
	}
	m.state.Busy = true
	m.wg.Add(1)
	m.state.Error = ""
	m.state.Phase = action
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	m.cancel = cancel
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer cancel()
		err := m.acquire(ctx)
		if err == nil {
			// A queued operation must see any node retired by the preceding lease.
			m.mu.Lock()
			next.Disabled = m.saved.Disabled
			next.UseOnce = m.saved.UseOnce
			m.mu.Unlock()
			err = m.run(ctx, action, next)
			m.release()
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		m.state.Busy = false
		if err != nil {
			m.state.Error = err.Error()
			// A failed subscription/configuration operation does not imply that
			// the previously loaded kernel stopped. Keep the runtime state
			// truthful while exposing the operation error to the admin UI.
			if m.state.Running {
				m.state.Phase = "running"
			} else {
				m.state.Phase = "failed"
			}
		} else if m.state.Running {
			m.state.Phase = "running"
		} else {
			m.state.Phase = "ready"
		}
	}()
	return nil
}

func normalizeURLs(raw []string) ([]string, error) {
	if len(raw) > 32 {
		return nil, errors.New("at most 32 subscriptions")
	}
	result := []string{}
	seen := map[string]bool{}
	for _, v := range raw {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		u, err := url.Parse(v)
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" || len(v) > 8192 {
			return nil, errors.New("invalid HTTP(S) subscription address")
		}
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result, nil
}

func (m *Manager) run(ctx context.Context, action string, next saved) error {
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		return errors.New("data directory is not writable")
	}
	if action == "install" {
		m.mu.Lock()
		installed := m.state.Installed
		m.mu.Unlock()
		if !installed {
			if err := m.install(ctx); err != nil {
				return err
			}
		}
		m.mu.Lock()
		m.state.Installed = true
		m.mu.Unlock()
		return nil
	}
	m.mu.Lock()
	installed := m.state.Installed
	old := m.saved
	m.mu.Unlock()
	if !installed {
		return errors.New("install the kernel first")
	}
	if action == "apply" || action == "apply_dynamic" {
		var nodes []map[string]any
		var names map[string]string
		if len(next.URLs) > 0 {
			var err error
			nodes, names, err = m.fetchNodes(ctx, next.URLs)
			if err != nil {
				return err
			}
		} else {
			nodes, names = []map[string]any{}, map[string]string{}
		}
		if len(next.DynamicProxies) > 0 {
			dynamicNodes, dynamicNames, err := dynamicProxyNodes(next.DynamicProxies)
			if err != nil {
				return err
			}
			nodes = append(nodes, dynamicNodes...)
			for name, display := range dynamicNames {
				names[name] = display
			}
		}
		if len(nodes) == 0 {
			return errors.New("save a valid subscription or dynamic proxy first")
		}
		next.Nodes = nodes
		next.NodeNames = names
	}
	if len(next.Nodes) == 0 {
		return errors.New("save a valid subscription first")
	}
	if action == "country_scan" || strings.HasPrefix(action, "country_probe/") {
		target := ""
		if strings.HasPrefix(action, "country_probe/") {
			target = strings.TrimPrefix(action, "country_probe/")
		}
		observations, err := m.scanCountries(ctx, next, target)
		if err != nil {
			return err
		}
		next.Countries = observations
	}
	if action == "once_on" {
		next.UseOnce = true
	}
	if action == "once_off" {
		next.UseOnce = false
	}
	if op, name, ok := strings.Cut(action, "/"); ok {
		found := false
		for _, n := range next.Nodes {
			if n["name"] == name {
				found = true
			}
		}
		if !found {
			return errors.New("unknown node")
		}
		states := map[string]string{}
		for k, v := range next.Disabled {
			states[k] = v
		}
		next.Disabled = states
		switch op {
		case "used":
			next.Disabled[name] = "used"
		case "failed":
			next.Disabled[name] = "failed"
		case "disable":
			next.Disabled[name] = "disabled"
		case "recover":
			delete(next.Disabled, name)
		case "probe":
			if err := m.control(ctx, http.MethodGet, "/proxies/"+url.PathEscape(name)+"/delay?timeout=1000&url=https%3A%2F%2Fwww.gstatic.com%2Fgenerate_204", next.Secret, nil); err != nil {
				next.Disabled[name] = "failed"
			} else {
				delete(next.Disabled, name)
			}
		}
	}
	if next.Secret == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return errors.New("cannot generate controller secret")
		}
		next.Secret = hex.EncodeToString(b)
	}
	candidate, err := m.config(next)
	if err != nil {
		return err
	}
	candidatePath := filepath.Join(m.dir, "candidate.json")
	if err = atomicWrite(candidatePath, candidate, 0600); err != nil {
		return errors.New("cannot write candidate configuration")
	}
	cmd := exec.CommandContext(ctx, filepath.Join(m.dir, "mihomo"), "-d", m.dir, "-f", candidatePath, "-t")
	if err = cmd.Run(); err != nil {
		return errors.New("kernel rejected subscription configuration")
	}
	m.mu.Lock()
	running := m.state.Running
	m.mu.Unlock()
	if running {
		if err = m.reload(ctx, candidate, old.Secret); err != nil {
			return errors.New("configuration reload failed; previous configuration retained")
		}
	} else if err = m.start(ctx, candidatePath, next.Secret); err != nil {
		return err
	}
	b, _ := json.Marshal(next)
	if err = atomicWrite(filepath.Join(m.dir, "settings.json"), b, 0600); err != nil {
		if running {
			previous, _ := m.config(old)
			_ = m.reload(ctx, previous, old.Secret)
		} else {
			m.stop()
		}
		return errors.New("cannot save configuration; change rolled back")
	}
	m.mu.Lock()
	m.saved = next
	m.mu.Unlock()
	return nil
}

func atomicWrite(path string, b []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func (m *Manager) get(ctx context.Context, address string, limit int64, ua string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("invalid download address")
	}
	req.Header.Set("User-Agent", ua)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, errors.New("download failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("download incomplete or too large")
	}
	return b, nil
}

func (m *Manager) getViaProxy(ctx context.Context, address string, limit int64, ua, proxyAddress string) ([]byte, error) {
	var proxyURL *url.URL
	if proxyAddress != "" {
		var err error
		proxyURL, err = url.Parse(proxyAddress)
		if err != nil || proxyURL.Scheme == "" || proxyURL.Host == "" {
			return nil, errors.New("invalid subscription proxy")
		}
	}
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("subscription proxy transport unavailable")
	}
	transport := baseTransport.Clone()
	transport.Proxy = nil // Explicit direct access must not inherit HTTP_PROXY.
	if proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	client := &http.Client{Transport: transport, Timeout: m.client.Timeout}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("invalid download address")
	}
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("download failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("download incomplete or too large")
	}
	return b, nil
}

func (m *Manager) install(ctx context.Context) error {
	asset := "mihomo-linux-" + runtime.GOARCH + "-" + Version + ".gz"
	if runtime.GOARCH == "amd64" {
		asset = "mihomo-linux-amd64-compatible-" + Version + ".gz"
	}
	manifest, err := m.get(ctx, "https://api.github.com/repos/MetaCubeX/mihomo/releases/tags/"+Version, 4<<20, "Sub2API")
	if err != nil {
		return err
	}
	var release struct {
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if json.Unmarshal(manifest, &release) != nil {
		return errors.New("invalid release manifest")
	}
	expected := ""
	for _, a := range release.Assets {
		if a.Name == asset {
			expected = strings.TrimPrefix(a.Digest, "sha256:")
		}
	}
	if len(expected) != 64 {
		return errors.New("verified checksum unavailable for kernel")
	}
	compressed, err := m.get(ctx, "https://github.com/MetaCubeX/mihomo/releases/download/"+Version+"/"+asset, 64<<20, "Sub2API")
	if err != nil {
		return err
	}
	hash := sha256.Sum256(compressed)
	if hex.EncodeToString(hash[:]) != expected {
		return errors.New("kernel checksum mismatch")
	}
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return errors.New("invalid kernel archive")
	}
	defer func() { _ = gz.Close() }()
	binary, err := io.ReadAll(io.LimitReader(gz, (192<<20)+1))
	if err != nil || len(binary) > 192<<20 {
		return errors.New("invalid kernel size")
	}
	if err = atomicWrite(filepath.Join(m.dir, "mihomo"), binary, 0700); err != nil {
		return errors.New("cannot install kernel")
	}
	return nil
}

func (m *Manager) fetchNodes(ctx context.Context, urls []string) ([]map[string]any, map[string]string, error) {
	nodes := []map[string]any{}
	names := map[string]string{}
	seen := map[string]bool{}
	proxy := ""
	m.mu.Lock()
	if m.state.Running {
		proxy = m.subscriptionProxyURL
		if proxy == "" {
			proxy = Endpoint
		}
	}
	m.mu.Unlock()
	for _, address := range urls {
		var b []byte
		var err error
		if proxy != "" {
			b, err = m.getViaProxy(ctx, address, 4<<20, "clash.meta", proxy)
			if err != nil && ctx.Err() == nil {
				// Subscription retrieval must work even when the current exit is
				// broken. This fallback never applies to harvest/model traffic.
				proxyErr := err
				b, err = m.getViaProxy(ctx, address, 4<<20, "clash.meta", "")
				if err != nil {
					err = fmt.Errorf("subscription download failed via proxy (%v) and direct (%v)", proxyErr, err)
				}
			}
		} else {
			b, err = m.getViaProxy(ctx, address, 4<<20, "clash.meta", "")
		}
		if err != nil {
			return nil, nil, err
		}
		var doc struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if yaml.Unmarshal(b, &doc) != nil || len(doc.Proxies) == 0 {
			return nil, nil, errors.New("subscription must contain Clash/Mihomo YAML proxies")
		}
		for _, node := range doc.Proxies {
			// Only outbound entries are imported; never accept a provider's listeners,
			// rules, external controller or executable configuration.
			kind, _ := node["type"].(string)
			if kind == "" || strings.EqualFold(kind, "direct") || strings.EqualFold(kind, "reject") {
				continue
			}
			displayName, _ := node["name"].(string)
			delete(node, "name")
			delete(node, "dialer-proxy")
			encoded, err := json.Marshal(node)
			if err != nil {
				return nil, nil, errors.New("invalid proxy entry")
			}
			hash := sha256.Sum256(encoded)
			id := hex.EncodeToString(hash[:])
			if seen[id] {
				continue
			}
			seen[id] = true
			nodeID := "node-" + id[:16]
			node["name"] = nodeID
			names[nodeID] = sanitizeNodeDisplayName(displayName)
			nodes = append(nodes, node)
			if len(nodes) > 1000 {
				return nil, nil, errors.New("at most 1000 nodes")
			}
		}
	}
	if len(nodes) == 0 {
		return nil, nil, errors.New("subscription has no usable nodes")
	}
	return nodes, names, nil
}

func (m *Manager) config(s saved) ([]byte, error) {
	names := []string{}
	for _, n := range s.Nodes {
		name, ok := n["name"].(string)
		if !ok || name == "" {
			return nil, errors.New("invalid saved node")
		}
		if s.Disabled[name] != "" || !countryAllowed(s, name) {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		names = []string{"REJECT"}
	}
	group := map[string]any{"name": "CODEX-ROTATE", "type": "load-balance", "strategy": "round-robin", "proxies": names, "url": "https://www.gstatic.com/generate_204", "interval": 300}
	if s.UseOnce {
		group["type"] = "select"
		delete(group, "strategy")
		delete(group, "url")
		delete(group, "interval")
	}
	groups := []any{group}
	laneNodes := []string{"REJECT"}
	for _, n := range s.Nodes {
		name, _ := n["name"].(string)
		if countryAllowed(s, name) && (s.Disabled[name] == "" || s.Disabled[name] == "used") {
			laneNodes = append(laneNodes, name)
		}
	}
	listeners := make([]any, 0, MaxCollectLanes)
	for lane := 0; lane < MaxCollectLanes; lane++ {
		groups = append(groups, map[string]any{"name": collectionGroup(lane), "type": "select", "proxies": laneNodes})
		listeners = append(listeners, map[string]any{"name": collectionGroup(lane), "type": "mixed", "listen": "127.0.0.1", "port": collectPort + lane, "proxy": collectionGroup(lane)})
	}
	bpsListeners, err := m.bpsListeners(s)
	if err != nil {
		return nil, err
	}
	listeners = append(listeners, bpsListeners...)
	return json.Marshal(map[string]any{"mixed-port": 3101, "allow-lan": false, "bind-address": "127.0.0.1", "mode": "rule", "log-level": "silent", "external-controller": "127.0.0.1:9098", "secret": s.Secret, "proxies": s.Nodes, "proxy-groups": groups, "listeners": listeners, "rules": []string{"MATCH,CODEX-ROTATE"}})
}

func (m *Manager) control(ctx context.Context, method, path, secret string, payload []byte) error {
	base := m.controllerURL
	if base == "" {
		base = "http://127.0.0.1:9098"
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("controller unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("controller rejected operation")
	}
	return nil
}
func (m *Manager) reload(ctx context.Context, b []byte, secret string) error {
	payload, _ := json.Marshal(map[string]string{"payload": string(b)})
	return m.control(ctx, http.MethodPut, "/configs?force=true", secret, payload)
}

func (m *Manager) start(ctx context.Context, path, secret string) error {
	ports := []string{"127.0.0.1:3101", "127.0.0.1:9098"}
	for lane := 0; lane < MaxCollectLanes; lane++ {
		ports = append(ports, fmt.Sprintf("127.0.0.1:%d", collectPort+lane))
	}
	m.bpsMu.Lock()
	for _, port := range m.bpsPorts {
		ports = append(ports, fmt.Sprintf("127.0.0.1:%d", port))
	}
	m.bpsMu.Unlock()
	for _, port := range ports {
		ln, err := net.Listen("tcp", port)
		if err != nil {
			return errors.New("proxy/controller port occupied; migrate the existing sidecar first")
		}
		_ = ln.Close()
	}
	cmd := exec.Command(filepath.Join(m.dir, "mihomo"), "-d", m.dir, "-f", path)
	configureChild(cmd)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("service is stopping")
	}
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		return errors.New("kernel failed to start")
	}
	m.cmd = cmd
	m.done = make(chan struct{})
	done := m.done
	m.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		m.mu.Lock()
		if m.cmd == cmd {
			m.state.Running = false
			if !m.closed {
				m.state.Phase = "stopped"
				m.state.Error = "kernel exited; check configuration and start again"
			}
		}
		close(done)
		m.mu.Unlock()
	}()
	for i := 0; i < 40; i++ {
		if m.control(ctx, http.MethodGet, "/version", secret, nil) == nil {
			conn, dialErr := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(ctx, "tcp", "127.0.0.1:3101")
			if dialErr == nil {
				_ = conn.Close()
				m.mu.Lock()
				select {
				case <-done:
					m.mu.Unlock()
					return errors.New("kernel exited during startup")
				default:
				}
				m.state.Running = true
				m.mu.Unlock()
				return nil
			}
		}
		select {
		case <-ctx.Done():
			m.stop()
			return errors.New("kernel startup cancelled")
		case <-done:
			return errors.New("kernel exited during startup")
		case <-time.After(100 * time.Millisecond):
		}
	}
	m.stop()
	return errors.New("kernel did not become ready")
}
func (m *Manager) stop() {
	m.mu.Lock()
	cmd, done := m.cmd, m.done
	m.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		<-done
	}
}
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
	m.stop()
}
