package serverless

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const forwardHeader = "X-Sub2api-Route"

type ticket struct {
	Node  string `json:"node"`
	Boot  string `json:"boot"`
	IP    string `json:"ip"`
	At    int64  `json:"at"`
	Nonce string `json:"nonce"`
	Sig   string `json:"sig"`
}

func failure(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "serverless_unavailable", "message": message}})
}
func (m *Manager) proof(t ticket, r *http.Request) string {
	return m.mac("forward", t.Node, t.Boot, t.IP, strconv.FormatInt(t.At, 10), t.Nonce, r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"), r.Header.Get("X-Goog-Api-Key"))
}

// Ingress validates forwarding before existing IP ACL / session middleware.
// A forged route marker never bypasses authentication or causes a second hop.
func (m *Manager) Ingress() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader(forwardHeader)
		if raw == "" {
			c.Next()
			return
		}
		var t ticket
		if len(raw) > 2048 || json.Unmarshal([]byte(raw), &t) != nil || !m.runtime.Gateway || t.Node != m.runtime.ID || t.Boot != m.boot || len(m.runtime.Secret) < 32 ||
			len(t.Nonce) != 32 || t.At < time.Now().Unix()-30 || t.At > time.Now().Unix()+5 {
			c.AbortWithStatus(403)
			return
		}
		if _, err := netip.ParseAddr(t.IP); err != nil || !m.verify(t.Sig, "forward", t.Node, t.Boot, t.IP, strconv.FormatInt(t.At, 10), t.Nonce, c.Request.Method, c.Request.URL.RequestURI(), c.GetHeader("Authorization"), c.GetHeader("X-Api-Key"), c.GetHeader("X-Goog-Api-Key")) {
			c.AbortWithStatus(403)
			return
		}
		if m.redis == nil {
			failure(c, "Routing replay protection unavailable")
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		ok, err := m.redis.SetNX(ctx, prefix+"nonce:"+t.Nonce, "1", time.Minute).Result()
		if err != nil {
			failure(c, "Routing replay protection unavailable")
			return
		}
		if !ok {
			c.AbortWithStatus(403)
			return
		}
		c.Set("serverless_verified_ip", t.IP)
		c.Request.Header.Del(forwardHeader)
		c.Next()
	}
}
func (m *Manager) ProbeHandler(c *gin.Context) {
	nonce := c.GetHeader("X-Serverless-Probe")
	if m.runtime.ID == "" || len(nonce) != 32 || !m.verify(c.GetHeader("X-Serverless-Proof"), "probe", m.runtime.ID, m.boot, nonce) {
		c.AbortWithStatus(403)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
	defer cancel()
	if m.ready == nil || m.ready(ctx) != nil {
		c.AbortWithStatus(503)
		return
	}
	c.Header("X-Serverless-Proof", m.mac("ready", m.runtime.ID, m.boot, nonce))
	c.Header("Cache-Control", "no-store")
	c.Status(200)
}

// Route only handles synchronous text APIs and Responses WebSocket handshakes.
// Existing API auth/body limits run first. Management and async task paths stay local.
func routePath(r *http.Request) bool {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if strings.HasSuffix(path, "/responses") {
		return r.Method == "POST" || r.Method == "GET"
	}
	return r.Method == "POST" && (strings.HasSuffix(path, "/messages") || strings.HasSuffix(path, "/chat/completions"))
}

func (m *Manager) affinityKey(cfg Config, keyID int64, clientIP string) string {
	if cfg.AffinityScope == AffinityKeyIP {
		// Use only the validated client IP supplied by gateway middleware. Never
		// store the raw IP or allow arbitrary malformed strings to grow the cache.
		ip, err := netip.ParseAddr(clientIP)
		normalized := "unknown"
		if err == nil {
			normalized = ip.Unmap().String()
		}
		return prefix + "affinity_ip:" + m.mac("key_ip", fmt.Sprint(keyID), normalized)
	}
	// Keep the original namespace for existing API Key bindings.
	return prefix + "affinity:" + m.mac("key", fmt.Sprint(keyID))
}

// Compare the exact binding before moving it. A concurrent request may already
// have made the same move; it must never overwrite a different/newer binding.
const fallbackBindingScript = `
local current=redis.call('GET',KEYS[1])
if current~=ARGV[1] and current~=ARGV[2] then return 0 end
redis.call('SET',KEYS[1],ARGV[2],'EX',ARGV[3])
return 1`

func (m *Manager) bindPrimary(ctx context.Context, key, previous, country string) bool {
	encoded, err := json.Marshal(Binding{ID: "primary", Country: country})
	if err != nil {
		return false
	}
	result, err := m.redis.Eval(ctx, fallbackBindingScript, []string{key}, previous, string(encoded), int(affinityTTL/time.Second)).Int()
	return err == nil && result == 1
}

func (m *Manager) Route(c *gin.Context, keyID int64, clientIP string) {
	if m.runtime.Gateway || keyID <= 0 || !routePath(c.Request) {
		c.Next()
		return
	}
	cfg, err := m.Config(c.Request.Context())
	if err != nil {
		failure(c, "Routing configuration unavailable")
		return
	}
	if !cfg.Enabled && !cfg.Established {
		c.Next()
		return
	}
	if len(m.runtime.Secret) < 32 || m.redis == nil {
		if cfg.Enabled {
			failure(c, "Routing credentials unavailable")
		} else {
			c.Next()
		}
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 4*time.Second)
	defer cancel()
	country := ""
	key := m.affinityKey(cfg, keyID, clientIP)
	raw, err := m.redis.Get(ctx, key).Result()
	reason := "binding"
	var b Binding
	if err == nil {
		if json.Unmarshal([]byte(raw), &b) != nil {
			failure(c, "Invalid Pod binding")
			return
		}
	} else if !errors.Is(err, redis.Nil) {
		failure(c, "Pod binding store unavailable")
		return
	} else {
		if !cfg.Enabled {
			c.Next()
			return
		}
		country = m.geo.Country(ctx, clientIP)
		b = Binding{ID: "primary"}
		reason = "default"
		ids := map[string]bool{}
		for _, r := range cfg.Regions {
			if r.Country == country {
				for _, id := range r.PodIDs {
					ids[id] = true
				}
			}
		}
		pods, e := m.Pods(ctx)
		if e != nil {
			failure(c, "Pod registry unavailable")
			return
		}
		// Rendezvous ordering is stable for a Key while healthy membership is unchanged.
		sort.Slice(pods, func(i, j int) bool { return m.mac("choose", key, pods[i].ID) > m.mac("choose", key, pods[j].ID) })
		attempts := 0
		for _, p := range pods {
			if !ids[p.ID] || !approved(cfg, p, true) || !p.Ready {
				continue
			}
			attempts++
			if attempts > 3 {
				break
			}
			if m.Probe(ctx, p) {
				b = Binding{ID: p.ID, Boot: p.Boot, Endpoint: p.Endpoint}
				reason = "region"
				break
			}
		}
		if b.ID == "primary" && len(ids) > 0 {
			reason = "unhealthy_fallback"
		}
		b.Country = country
		encoded, _ := json.Marshal(b)
		raw = string(encoded)
		won, e := m.redis.SetNX(ctx, key, encoded, affinityTTL).Result()
		if e != nil {
			failure(c, "Could not bind request to a Pod")
			return
		}
		if !won {
			value, e := m.redis.Get(ctx, key).Result()
			if e != nil || json.Unmarshal([]byte(value), &b) != nil {
				failure(c, "Pod binding unavailable")
				return
			}
			raw = value
			reason = "binding"
		}
	}
	country = b.Country
	// Refresh on use; long lived conversations do not migrate on config updates.
	if err = m.redis.Expire(ctx, key, affinityTTL).Err(); err != nil {
		failure(c, "Could not preserve Pod binding")
		return
	}
	if b.ID == "primary" {
		c.Header("X-Sub2api-Pod", "primary")
		c.Next()
		m.Record(c.Request.Context(), "primary", country, reason, c.Writer.Status())
		return
	}
	pods, err := m.Pods(ctx)
	if err != nil {
		failure(c, "Pod registry unavailable")
		return
	}
	var target *Pod
	for i := range pods {
		p := &pods[i]
		if p.ID == b.ID && p.Boot == b.Boot && p.Endpoint == b.Endpoint && approved(cfg, *p, false) {
			target = p
			break
		}
	}
	if target == nil || !m.Probe(ctx, *target) {
		// This decision runs before forwarding any business request. Failures
		// after forward() starts are never replayed on the primary.
		if cfg.FallbackToPrimary && m.bindPrimary(ctx, key, raw, country) {
			c.Header("X-Sub2api-Pod", "primary")
			c.Next()
			m.Record(c.Request.Context(), "primary", country, "binding_fallback", c.Writer.Status())
			return
		}
		m.Record(c.Request.Context(), b.ID, country, "binding_unavailable", 503)
		failure(c, "Bound Pod unavailable; the existing conversation was not moved")
		return
	}
	m.forward(c, *target, clientIP)
	m.Record(c.Request.Context(), target.ID, country, reason, c.Writer.Status())
}
func approved(cfg Config, p Pod, newBinding bool) bool {
	for _, policy := range cfg.Pods {
		if policy.ID == p.ID && strings.TrimRight(policy.Endpoint, "/") == p.Endpoint && (!newBinding || policy.Enabled) {
			return true
		}
	}
	return false
}
func (m *Manager) forward(c *gin.Context, p Pod, clientIP string) {
	target, err := url.Parse(p.Endpoint)
	if err != nil {
		failure(c, "Invalid Pod endpoint")
		return
	}
	c.Abort()
	proxy := &httputil.ReverseProxy{
		Transport: m.transport, FlushInterval: -1,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = r.In.Host // Preserve public origin for generated capability URLs.
			r.Out.Header.Del("Forwarded")
			r.Out.Header.Del("X-Forwarded-For")
			r.Out.Header.Del("X-Real-IP")
			r.Out.Header.Set("X-Forwarded-For", clientIP)
			r.Out.Header.Set("X-Real-IP", clientIP)
			// Target ignores untrusted IP headers in favour of this signed identity.
			t := ticket{Node: p.ID, Boot: p.Boot, IP: clientIP, At: time.Now().Unix(), Nonce: randomID()}
			t.Sig = m.proof(t, r.Out)
			encoded, _ := json.Marshal(t)
			r.Out.Header.Set(forwardHeader, string(encoded))
		},
		ModifyResponse: func(r *http.Response) error { r.Header.Set("X-Sub2api-Pod", p.ID); return nil },
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(503)
			_, _ = w.Write([]byte("{\"error\":{\"type\":\"serverless_unavailable\",\"message\":\"Pod transfer failed; request was not retried\"}}"))
		},
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}

// ImageOwnerURL keeps BPS capability downloads on the generating instance.
func (m *Manager) ImageOwnerURL(raw string) string {
	if m.runtime.ID == "" || len(m.runtime.Secret) < 32 {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("sl_node", m.runtime.ID)
	q.Set("sl_boot", m.boot)
	q.Set("sl_proof", m.mac("image", m.runtime.ID, m.boot, u.Path))
	u.RawQuery = q.Encode()
	return u.String()
}
func (m *Manager) ImageRoute(c *gin.Context) {
	id, boot, proof := c.Query("sl_node"), c.Query("sl_boot"), c.Query("sl_proof")
	if id == "" && boot == "" && proof == "" {
		c.Next()
		return
	}
	if !identifier.MatchString(id) || len(boot) != 32 || !m.verify(proof, "image", id, boot, c.Request.URL.Path) {
		c.AbortWithStatus(404)
		return
	}
	if m.runtime.ID == id && m.boot == boot {
		c.Next()
		return
	}
	if m.runtime.Gateway {
		c.AbortWithStatus(404)
		return
	}
	cfg, err := m.Config(c.Request.Context())
	if err != nil {
		c.AbortWithStatus(503)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 4*time.Second)
	defer cancel()
	pods, err := m.Pods(ctx)
	if err != nil {
		c.AbortWithStatus(503)
		return
	}
	for _, p := range pods {
		if p.ID == id && p.Boot == boot && approved(cfg, p, false) && m.Probe(ctx, p) {
			clientIP, _, _ := net.SplitHostPort(c.Request.RemoteAddr)
			if _, err := netip.ParseAddr(clientIP); err != nil {
				clientIP = "127.0.0.1"
			}
			m.forward(c, p, clientIP)
			return
		}
	}
	c.AbortWithStatus(503)
}
