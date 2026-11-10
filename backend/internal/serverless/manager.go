package serverless

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const prefix = "serverless:v1:"
const affinityTTL = 24 * time.Hour

type Pod struct {
	ID        string `json:"id"`
	Boot      string `json:"boot"`
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	Version   string `json:"version"`
	Ready     bool   `json:"ready"`
	At        int64  `json:"at"`
	Signature string `json:"-"`
}
type podEnvelope struct {
	Pod       Pod    `json:"pod"`
	Signature string `json:"signature"`
}
type Binding struct {
	Country  string `json:"country"`
	ID       string `json:"id"`
	Boot     string `json:"boot"`
	Endpoint string `json:"endpoint"`
}
type Manager struct {
	geo       CountryResolver
	runtime   Runtime
	redis     *redis.Client
	store     Store
	boot      string
	http      *http.Client
	transport *http.Transport
	logger    *slog.Logger
	mu        sync.Mutex
	config    Config
	expires   time.Time
	loadErr   error
	stop      context.CancelFunc
	done      chan struct{}
	ready     func(context.Context) error
	healthMu  sync.Mutex
	health    map[string]healthResult
}
type healthResult struct {
	Until   time.Time
	Healthy bool
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func New(r Runtime, cache *redis.Client, store Store) (*Manager, error) {
	if err := ValidateRuntime(r); err != nil {
		return nil, err
	}
	tr := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: r.ResponseHeaderTimeout, MaxIdleConns: 128, MaxIdleConnsPerHost: 16, IdleConnTimeout: time.Minute}
	m := &Manager{runtime: r, redis: cache, store: store, boot: randomID(), transport: tr, logger: slog.Default(), health: map[string]healthResult{}}
	m.geo = NewGeoJSResolver(cache)
	// Probes retain their own short total budget, independent of business waits.
	probeTransport := tr.Clone()
	probeTransport.ResponseHeaderTimeout = 0
	m.http = &http.Client{Transport: probeTransport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return m, nil
}
func (m *Manager) Config(ctx context.Context) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Now().Before(m.expires) {
		return m.config, m.loadErr
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c, err := m.store.Load(ctx)
	if err == nil {
		err = Validate(c)
	}
	m.config = c
	m.loadErr = err
	m.expires = time.Now().Add(5 * time.Second)
	return c, err
}
func (m *Manager) Save(ctx context.Context, c Config) error {
	if err := Validate(c); err != nil {
		return err
	}
	if c.Enabled && (len(m.runtime.Secret) < 32 || m.redis == nil) {
		return errors.New("configure the cluster secret and Redis before enabling routing")
	}
	previous, err := m.store.Load(ctx)
	if err != nil {
		return err
	}
	c.Established = previous.Established || previous.Enabled || c.Enabled
	if err := m.store.Save(ctx, c); err != nil {
		return err
	}
	m.mu.Lock()
	m.expires = time.Time{}
	m.mu.Unlock()
	return nil
}
func (m *Manager) mac(parts ...string) string {
	h := hmac.New(sha256.New, []byte(m.runtime.Secret))
	for _, s := range parts {
		fmt.Fprintf(h, "%d:", len(s))
		_, _ = io.WriteString(h, s)
	}
	return hex.EncodeToString(h.Sum(nil))
}
func (m *Manager) verify(sig string, parts ...string) bool {
	return len(m.runtime.Secret) >= 32 && hmac.Equal([]byte(sig), []byte(m.mac(parts...)))
}
func (m *Manager) Start(ready func(context.Context) error) {
	m.ready = ready
	if m.runtime.ID == "" || m.redis == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.stop = cancel
	m.done = make(chan struct{})
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			m.heartbeat(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (m *Manager) Stop() {
	if m.stop != nil {
		m.stop()
		<-m.done
	}
	m.transport.CloseIdleConnections()
	m.http.CloseIdleConnections()
}
func (m *Manager) heartbeat(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	ready := m.ready != nil && m.ready(ctx) == nil
	pod := Pod{ID: m.runtime.ID, Boot: m.boot, Endpoint: strings.TrimRight(m.runtime.Endpoint, "/"), Region: m.runtime.Region, Version: m.runtime.Version, Ready: ready, At: time.Now().Unix()}
	b, _ := json.Marshal(pod)
	envelope, _ := json.Marshal(podEnvelope{Pod: pod, Signature: m.mac("pod", string(b))})
	// A fresh heartbeat owned by another boot must not be overwritten.
	_, _ = m.redis.Eval(ctx, registrationScript, []string{prefix + "pod:" + pod.ID, prefix + "pods"}, m.boot, pod.At, string(envelope), pod.ID).Result()
}
func (m *Manager) Pods(ctx context.Context) ([]Pod, error) {
	if m.redis == nil {
		return []Pod{}, nil
	}
	ids, err := m.redis.ZRevRange(ctx, prefix+"pods", 0, 127).Result() //nolint:staticcheck // Redis 6.0 compatibility; replacement options require Redis 6.2.
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []Pod{}, nil
	}
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, prefix+"pod:"+id)
	}
	values, err := m.redis.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	out := make([]Pod, 0, len(values))
	for _, v := range values {
		raw, ok := v.(string)
		if !ok {
			continue
		}
		var e podEnvelope
		if json.Unmarshal([]byte(raw), &e) != nil {
			continue
		}
		b, _ := json.Marshal(e.Pod)
		if !m.verify(e.Signature, "pod", string(b)) || !identifier.MatchString(e.Pod.ID) || ValidateEndpoint(e.Pod.Endpoint) != nil {
			continue
		}
		if time.Now().Unix()-e.Pod.At > 35 || e.Pod.At > time.Now().Unix()+10 {
			e.Pod.Ready = false
		}
		out = append(out, e.Pod)
	}
	return out, nil
}
func (m *Manager) Probe(ctx context.Context, p Pod) bool {
	if !p.Ready {
		return false
	}
	key := p.ID + "/" + p.Boot + "/" + p.Endpoint
	m.healthMu.Lock()
	if h, ok := m.health[key]; ok && time.Now().Before(h.Until) {
		m.healthMu.Unlock()
		return h.Healthy
	}
	m.healthMu.Unlock()
	nonce := randomID()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Endpoint+"/internal/serverless/probe", nil)
	if err != nil {
		return false
	}
	request.Header.Set("X-Serverless-Probe", nonce)
	request.Header.Set("X-Serverless-Proof", m.mac("probe", p.ID, p.Boot, nonce))
	response, err := m.http.Do(request)
	healthy := false
	if err == nil {
		defer func() { _ = response.Body.Close() }()
		healthy = response.StatusCode == 200 && m.verify(response.Header.Get("X-Serverless-Proof"), "ready", p.ID, p.Boot, nonce)
	}
	m.healthMu.Lock()
	if len(m.health) > 256 {
		clear(m.health)
	}
	m.health[key] = healthResult{Until: time.Now().Add(5 * time.Second), Healthy: healthy}
	m.healthMu.Unlock()
	return healthy
}
func (m *Manager) Record(ctx context.Context, pod, country, reason string, status int) {
	if m.redis == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 300*time.Millisecond)
	defer cancel()
	key := prefix + "stats:" + time.Now().UTC().Format("2006-01-02")
	if country == "" {
		country = "unknown"
	}
	pipe := m.redis.TxPipeline()
	field := pod + "|" + country + "|" + reason
	pipe.HIncrBy(ctx, key, field+"|requests", 1)
	if status >= 400 {
		pipe.HIncrBy(ctx, key, field+"|errors", 1)
	}
	pipe.Expire(ctx, key, 8*24*time.Hour)
	_, _ = pipe.Exec(ctx)
}
func (m *Manager) Stats(ctx context.Context) (map[string]string, error) {
	if m.redis == nil {
		return map[string]string{}, nil
	}
	return m.redis.HGetAll(ctx, prefix+"stats:"+time.Now().UTC().Format("2006-01-02")).Result()
}

const registrationScript = `
local raw=redis.call('GET',KEYS[1])
if raw then
 local ok,old=pcall(cjson.decode,raw)
 if ok and old.pod and old.pod.boot ~= ARGV[1] and tonumber(old.pod.at) > tonumber(ARGV[2])-35 then return 0 end
end
redis.call('SET',KEYS[1],ARGV[3],'EX',86400)
redis.call('ZADD',KEYS[2],ARGV[2],ARGV[4])
redis.call('ZREMRANGEBYSCORE',KEYS[2],'-inf',tonumber(ARGV[2])-86400)
return 1
`
