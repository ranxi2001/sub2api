package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const (
	openAICodexTicketExtraKeyPrefix  = "codex_turn_ticket:"
	OpenAICodexSkipHarvestExtraKey   = "codex_skip_harvest"
	openAICodexAstraMinVersion       = "0.153.4"
	openAICodexTicketStatePrefix     = "gAAAAA"
	openAICodexTicketDefaultModel    = "gpt-6-astra"
	openAICodexTicketDefaultSolModel = "gpt-5.6-sol"
	openAICodexTicketPersonalBlocks  = 10
	openAICodexTicketTeamBlocks      = 12
	openAICodexCredentialTTL         = 240 * time.Second
)

// ErrOpenAICodexTicketUnavailable 表示该号该模型没有可用的 292 门票，
// 且 fail_closed 禁止裸打业务请求。
var ErrOpenAICodexTicketUnavailable = errors.New("codex turn-state ticket unavailable")

type openAICodexTicket struct {
	AccountID           int64              `json:"account_id"`
	Model               string             `json:"model"`
	State               string             `json:"state"`
	Length              int                `json:"length"`
	CapturedAt          time.Time          `json:"captured_at"`
	ExpiresAt           time.Time          `json:"expires_at"`
	Attempts            int                `json:"attempts"`
	Blocks              int                `json:"blocks,omitempty"`
	IssuedAt            time.Time          `json:"issued_at,omitempty"`
	Identity            string             `json:"identity,omitempty"`
	HarvestProxyURL     string             `json:"harvest_proxy_url,omitempty"`
	HarvestNodeID       string             `json:"harvest_node_id,omitempty"`
	HarvestNodeName     string             `json:"harvest_node_name,omitempty"`
	HarvestNodeProvider string             `json:"harvest_node_provider,omitempty"`
	HarvestSessionID    string             `json:"harvest_session_id,omitempty"`
	EdgeIP              string             `json:"edge_ip,omitempty"`
	Transport           string             `json:"transport,omitempty"`
	Gateway             string             `json:"gateway,omitempty"`
	HarvestLite         bool               `json:"harvest_lite,omitempty"`
	HarvestCookies      []string           `json:"harvest_cookies,omitempty"`
	HarvestCookiesAt    time.Time          `json:"harvest_cookies_at,omitempty"`
	Standby             *openAICodexTicket `json:"standby,omitempty"`
	Revoked             bool               `json:"revoked,omitempty"`
}

func codexTicketCookiesFresh(ticket *openAICodexTicket, now time.Time) bool {
	if ticket == nil || len(ticket.HarvestCookies) == 0 {
		return false
	}
	if ticket.Length == 780 {
		_, exp, err := codex780Route(ticket.HarvestCookies, ticket.Gateway, now)
		return err == nil && now.Before(exp)
	}
	captured := ticket.HarvestCookiesAt
	if captured.IsZero() {
		// Backward compatibility for tickets stored before cookie timestamps
		// were separated from the turn-state capture timestamp.
		captured = ticket.CapturedAt
	}
	return !captured.IsZero() && now.Before(captured.Add(openAICodexCredentialTTL))
}

func codexTicketCookiesExpiry(ticket *openAICodexTicket) time.Time {
	if ticket == nil || len(ticket.HarvestCookies) == 0 {
		return time.Time{}
	}
	if ticket.Length == 780 {
		_, exp, _ := codex780Route(ticket.HarvestCookies, ticket.Gateway, time.Unix(0, 0))
		return exp
	}
	captured := ticket.HarvestCookiesAt
	if captured.IsZero() {
		captured = ticket.CapturedAt
	}
	if captured.IsZero() {
		return time.Time{}
	}
	return captured.Add(openAICodexCredentialTTL)
}

type openAICodexTicketShape struct {
	Blocks   int
	IssuedAt time.Time
}

func parseOpenAICodexTicketShape(value string) (openAICodexTicketShape, error) {
	value = strings.TrimSpace(value)
	if len(value) > 2048 || strings.ContainsAny(value, "\r\n\t ") {
		return openAICodexTicketShape{}, errors.New("invalid state encoding")
	}
	core := strings.TrimRight(value, "=")
	if len(value)-len(core) > 2 {
		return openAICodexTicketShape{}, errors.New("invalid state padding")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(core)
	if err != nil || len(raw) < 73 || raw[0] != 0x80 || (len(raw)-57)%16 != 0 {
		return openAICodexTicketShape{}, errors.New("unrecognized state envelope")
	}
	issuedUnix := binary.BigEndian.Uint64(raw[1:9])
	if issuedUnix < 1577836800 || issuedUnix >= 4102444800 {
		return openAICodexTicketShape{}, errors.New("state timestamp out of range")
	}
	return openAICodexTicketShape{Blocks: (len(raw) - 57) / 16, IssuedAt: time.Unix(int64(issuedUnix), 0)}, nil
}

// openAICodexTicketTeamPlanMarkers identify ChatGPT Team/Business/Enterprise
// subscriptions. Upstream does not report a single canonical plan_type: besides
// "team" it also emits variants such as "self_serve_business_prolite", so these
// plans are detected by substring instead of equality.
var openAICodexTicketTeamPlanMarkers = []string{"team", "business", "enterprise"}

func openAICodexTicketIsTeamPlan(account *Account) bool {
	if account == nil {
		return false
	}
	plan := strings.ToLower(strings.TrimSpace(account.GetCredential("plan_type")))
	if plan == "" {
		return false
	}
	for _, marker := range openAICodexTicketTeamPlanMarkers {
		if strings.Contains(plan, marker) {
			return true
		}
	}
	return false
}

func openAICodexTicketExpectedBlocks(account *Account) int {
	if openAICodexTicketIsTeamPlan(account) {
		return openAICodexTicketTeamBlocks
	}
	return openAICodexTicketPersonalBlocks
}

func openAICodexTicketExpectedLength(account *Account) int {
	return base64.URLEncoding.EncodedLen(57 + 16*openAICodexTicketExpectedBlocks(account))
}

func openAICodexTicketTargetLength(account *Account, cfg config.OpenAICodexTicketConfig) int {

	if cfg.TargetLength > 0 && cfg.TargetLength != 292 {
		return cfg.TargetLength
	}
	return openAICodexTicketExpectedLength(account)
}

func openAICodexTicketKey(accountID int64, model string) string {
	return fmt.Sprintf("%d\x00%s", accountID, strings.TrimSpace(model))
}

func openAICodexTicketExtraKey(model string) string {
	return openAICodexTicketExtraKeyPrefix + strings.TrimSpace(model)
}

func normalizeOpenAICodexTicketModel(model string) string {
	return strings.TrimSpace(model)
}

// NormalizeOpenAICodexTicketModels removes empty/duplicate model names while
// preserving the configured order. An empty result is meaningful: it disables
// ticket gating for every model while leaving the global feature enabled.
func NormalizeOpenAICodexTicketModels(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = normalizeOpenAICodexTicketModel(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}

func extractOpenAICodexTicketModel(body []byte) string {
	return normalizeOpenAICodexTicketModel(gjson.GetBytes(body, "model").String())
}

func (s *OpenAIGatewayService) openAICodexTicketConfig() config.OpenAICodexTicketConfig {
	cfg := config.OpenAICodexTicketConfig{}
	if s != nil && s.cfg != nil {
		cfg = s.cfg.Gateway.OpenAICodexTicket
	}
	if cfg.TargetLength <= 0 {
		cfg.TargetLength = 292
	}
	if cfg.TTLSeconds <= 0 {
		cfg.TTLSeconds = 3600
	}
	if cfg.RefreshBeforeSeconds <= 0 {
		cfg.RefreshBeforeSeconds = 600
	}
	if cfg.HarvestProbeIntervalSeconds < 30 {
		cfg.HarvestProbeIntervalSeconds = 180
	}
	if cfg.HarvestCooldownSeconds <= 0 {
		cfg.HarvestCooldownSeconds = 180
	}
	if cfg.MaxProbesPerRound <= 0 {
		cfg.MaxProbesPerRound = 6
	}
	if cfg.HarvestAttemptTimeoutSeconds <= 0 {
		cfg.HarvestAttemptTimeoutSeconds = 25
	}
	if cfg.Models == nil {
		cfg.Models = []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}
	}
	if s != nil && s.settingService != nil {
		cfg.Models = s.settingService.GetOpenAICodexTicketModels(context.Background(), cfg.Models)
		cfg.FailClosed = s.settingService.GetOpenAICodexTicketFailClosed(context.Background())
	}
	return cfg
}

func (s *OpenAIGatewayService) openAICodexTicketGatedModel(model string) bool {
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" || !s.openAICodexTicketEnabled() {
		return false
	}
	for _, item := range s.openAICodexTicketConfig().Models {
		if normalizeOpenAICodexTicketModel(item) == model {
			return true
		}
	}
	return false
}

// OpenAICodexTicketStatus 是给管理端看的门票摘要，不含 state blob。
type OpenAICodexTicketStatus struct {
	Transport        string             `json:"transport,omitempty"`
	Gateway          string             `json:"gateway,omitempty"`
	EdgeIP           string             `json:"edge_ip,omitempty"`
	Model            string             `json:"model"`
	Length           int                `json:"length,omitempty"`
	Ready            bool               `json:"ready"`
	RemainingSeconds int64              `json:"remaining_seconds"`
	Blocked          bool               `json:"blocked"`
	ExpiresAt        *time.Time         `json:"expires_at,omitempty"`
	StandbyExpiresAt *time.Time         `json:"standby_expires_at,omitempty"`
	Standby          bool               `json:"standby,omitempty"`
	CookieCount      int                `json:"cookie_count,omitempty"`
	CookieExpiresAt  *time.Time         `json:"cookie_expires_at,omitempty"`
	Probe            *CodexProbeSummary `json:"probe,omitempty"`
}

func OpenAICodexTicketStatuses(account *Account, cfg config.OpenAICodexTicketConfig, now time.Time) []OpenAICodexTicketStatus {
	if !cfg.Enabled || !isOpenAICodexTicketAccount(account) {
		return []OpenAICodexTicketStatus{}
	}
	models, targetLen := cfg.Models, openAICodexTicketTargetLength(account, cfg)
	if models == nil {
		models = []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}
	}
	if targetLen <= 0 {
		targetLen = 292
	}
	out := make([]OpenAICodexTicketStatus, 0, len(models))
	for _, model := range models {
		model = normalizeOpenAICodexTicketModel(model)
		if model == "" || !isOpenAICodexTicketAccount(account, model) {
			continue
		}
		status := OpenAICodexTicketStatus{Model: model}
		status.Probe = readCodexProbe(account, model)
		ticket := parseOpenAICodexTicketFromAny(0, model, nil)
		if account != nil && account.Extra != nil {
			ticket = parseOpenAICodexTicketFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
		}
		if ticket != nil && !ticketIdentityMatches(account, ticket) {
			ticket = nil
		}
		usingStandby := false
		if ticket != nil && ticket.Standby.valid(now, targetLen) {
			standbyExp := openAICodexTicketRemainingUntil(ticket.Standby)
			status.StandbyExpiresAt = &standbyExp
			if !ticket.valid(now, targetLen) {
				ticket = ticket.Standby
				status.StandbyExpiresAt = nil
				usingStandby = true
			}
		}
		if ticket.valid(now, targetLen) {
			status.Transport = ticket.Transport
			status.Gateway = ticket.Gateway
			status.EdgeIP = ticket.EdgeIP
			status.Ready = true
			status.Standby = usingStandby
			status.Length = ticket.Length
			exp := openAICodexTicketRemainingUntil(ticket)
			remaining := int64(exp.Sub(now) / time.Second)
			if remaining < 0 {
				remaining = 0
			}
			status.RemainingSeconds = remaining
			status.ExpiresAt = &exp
		}
		if cookieExpiry := codexTicketCookiesExpiry(ticket); !cookieExpiry.IsZero() {
			status.CookieCount = len(ticket.HarvestCookies)
			status.CookieExpiresAt = &cookieExpiry
		}
		// Skip-harvest only stops probing. Missing tickets must not look like
		// a paused/降智 account; fail-closed still applies to harvest accounts.
		status.Blocked = cfg.FailClosed && !status.Ready && !openAICodexSkipHarvest(account)
		out = append(out, status)
	}
	return out
}

func (s *OpenAIGatewayService) openAICodexTicketEnabled() bool {
	return s.openAICodexTicketEnabledContext(context.Background())
}

func (s *OpenAIGatewayService) openAICodexTicketEnabledContext(ctx context.Context) bool {
	if s == nil {
		return false
	}
	fallback := s.cfg != nil && s.cfg.Gateway.OpenAICodexTicket.Enabled
	if s.settingService != nil {
		return s.settingService.GetOpenAICodexTicketEnabled(ctx, fallback)
	}
	return fallback
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestProxyURL() string {
	return s.openAICodexTicketHarvestProxyURLContext(context.Background())
}

// openAICodexTicketHarvestProxySetting returns the configured harvest exit
// verbatim, including the IP-pool sentinel.
func (s *OpenAIGatewayService) openAICodexTicketHarvestProxySetting(ctx context.Context) string {
	if s.settingService != nil {
		if proxy := s.settingService.GetOpenAICodexTicketHarvestProxyURL(ctx); proxy != "" {
			return proxy
		}
	}
	return strings.TrimSpace(s.openAICodexTicketConfig().HarvestProxyURL)
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestIPPoolEnabled(ctx context.Context) bool {
	return s.openAICodexTicketHarvestProxySetting(ctx) == OpenAICodexTicketHarvestIPPoolURL
}

// A pool-harvested ticket records its concrete exit, so request pinning never
// needs the pool itself. Without a recorded exit it must fall back to the
// account's own proxy rather than an arbitrary pool member.
func (s *OpenAIGatewayService) openAICodexTicketHarvestFixedProxyURL(ctx context.Context) string {
	proxy := s.openAICodexTicketHarvestProxySetting(ctx)
	if proxy == OpenAICodexTicketHarvestIPPoolURL {
		return ""
	}
	return proxy
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestProxyURLContext(ctx context.Context) string {
	proxy := s.openAICodexTicketHarvestProxySetting(ctx)
	if proxy == OpenAICodexTicketHarvestIPPoolURL {
		exit, _ := s.pickHarvestIPPoolExit(ctx, nil)
		return exit.url
	}
	return proxy
}

type harvestIPPoolExit struct {
	url  string
	name string
}

// Membership is cached with a short TTL and rotated round-robin, so consecutive
// harvest attempts spread across the whole active pool without a per-attempt DB
// read. An empty pool yields no exit, which callers treat as "skip this round"
// rather than harvesting on a direct connection.
const harvestIPPoolRefreshTTL = 15 * time.Second

func (s *OpenAIGatewayService) pickHarvestIPPoolExit(ctx context.Context, tried map[string]bool) (harvestIPPoolExit, bool) {
	s.refreshHarvestIPPool(ctx)
	s.harvestIPPoolMu.Lock()
	exits := s.harvestIPPoolExits
	s.harvestIPPoolMu.Unlock()
	if len(exits) == 0 {
		return harvestIPPoolExit{}, false
	}
	start := s.harvestIPPoolCursor.Add(1) - 1
	for i := range exits {
		exit := exits[(start+uint64(i))%uint64(len(exits))]
		if !tried[exit.url] {
			return exit, true
		}
	}
	return harvestIPPoolExit{}, false
}

func (s *OpenAIGatewayService) harvestIPPoolMember(ctx context.Context, proxyURL string) (harvestIPPoolExit, bool) {
	s.refreshHarvestIPPool(ctx)
	s.harvestIPPoolMu.Lock()
	defer s.harvestIPPoolMu.Unlock()
	for _, exit := range s.harvestIPPoolExits {
		if exit.url == proxyURL {
			return exit, true
		}
	}
	return harvestIPPoolExit{}, false
}

// pickHarvestIPPoolExitFor keeps a refresh on the exit its ticket was
// harvested from while that proxy is still an active pool member, then rotates.
func (s *OpenAIGatewayService) pickHarvestIPPoolExitFor(ctx context.Context, account *Account, model string, tried map[string]bool) (harvestIPPoolExit, bool) {
	if pinned := s.lookupOpenAICodexTicket(account, model); pinned != nil {
		if url := strings.TrimSpace(pinned.HarvestProxyURL); url != "" && !tried[url] {
			if exit, ok := s.harvestIPPoolMember(ctx, url); ok {
				return exit, true
			}
		}
	}
	return s.pickHarvestIPPoolExit(ctx, tried)
}

func (s *OpenAIGatewayService) refreshHarvestIPPool(ctx context.Context) {
	if s.proxyRepo == nil {
		return
	}
	now := time.Now()
	s.harvestIPPoolMu.Lock()
	fresh := now.Before(s.harvestIPPoolSyncedAt.Add(harvestIPPoolRefreshTTL))
	if !fresh {
		s.harvestIPPoolSyncedAt = now
	}
	s.harvestIPPoolMu.Unlock()
	if fresh {
		return
	}
	proxies, err := s.proxyRepo.ListActive(ctx)
	if err != nil {
		// Keep the last membership on a transient listing error rather than
		// collapsing the pool to empty and skipping every harvest round.
		logger.FromContext(ctx).Warn("openai_codex_ticket.ip_pool_refresh_failed", zap.Error(err))
		return
	}
	exits := make([]harvestIPPoolExit, 0, len(proxies))
	for i := range proxies {
		if proxies[i].IsExpired(now) {
			continue
		}
		exits = append(exits, harvestIPPoolExit{url: proxies[i].URL(), name: strings.TrimSpace(proxies[i].Name)})
	}
	s.harvestIPPoolMu.Lock()
	s.harvestIPPoolExits = exits
	s.harvestIPPoolMu.Unlock()
}

func openAICodexTicketRemainingUntil(t *openAICodexTicket) time.Time {
	if t == nil {
		return time.Time{}
	}
	exp := t.ExpiresAt
	if !t.IssuedAt.IsZero() {
		issuedExpiry := t.IssuedAt.Add(codexTicketLifetime(t.Length))
		if exp.IsZero() || issuedExpiry.Before(exp) {
			exp = issuedExpiry
		}
	}
	if t.Length == 780 {
		cookieExpiry := codexTicketCookiesExpiry(t)
		if !cookieExpiry.IsZero() && (exp.IsZero() || cookieExpiry.Before(exp)) {
			exp = cookieExpiry
		}
	}
	return exp
}

func (t *openAICodexTicket) valid(now time.Time, targetLen int) bool {
	if t != nil && t.Revoked {
		return false
	}
	if t == nil {
		return false
	}
	if t.Length == 780 {
		shape, err := parseOpenAICodexTicketShape(t.State)
		if err != nil || shape.IssuedAt.After(now.Add(30*time.Second)) || !now.Before(shape.IssuedAt.Add(openAICodexCredentialTTL)) || (t.Transport != "sse" && t.Transport != "websocket") || !codexTicketCookiesFresh(t, now) {
			return false
		}
		if _, _, err := codex780Route(t.HarvestCookies, t.Gateway, now); err != nil {
			return false
		}
	}
	state := strings.TrimSpace(t.State)
	if len(state) != targetLen || t.Length != targetLen || !strings.HasPrefix(state, openAICodexTicketStatePrefix) {
		return false
	}
	if t.ExpiresAt.IsZero() || !now.Before(t.ExpiresAt) {
		return false
	}
	if !t.IssuedAt.IsZero() && (t.IssuedAt.After(now.Add(30*time.Second)) || !now.Before(t.IssuedAt.Add(codexTicketLifetime(t.Length)))) {
		return false
	}
	return true
}

func (t *openAICodexTicket) needsRefresh(now time.Time, refreshBefore time.Duration) bool {
	if t == nil || t.ExpiresAt.IsZero() {
		return true
	}
	if t.Length == 780 && refreshBefore > 60*time.Second {
		refreshBefore = 60 * time.Second
	}
	return !openAICodexTicketRemainingUntil(t).After(now.Add(refreshBefore))
}

func (s *OpenAIGatewayService) lookupOpenAICodexTicket(account *Account, model string) *openAICodexTicket {
	if s == nil {
		return nil
	}
	s.openaiCodexTicketStateMu.Lock()
	defer s.openaiCodexTicketStateMu.Unlock()
	return s.lookupCodexTicketLocked(account, model)
}

// Keep the production identity binding: candidates are hydrated by the
// scheduler before admission, so missing identity must not bypass the gate.
func ticketIdentity(account *Account) string {
	if account == nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(account.GetCredential("chatgpt_account_id")+"\x00"+account.GetCredential("email"))))
}
func ticketIdentityMatches(account *Account, ticket *openAICodexTicket) bool {
	return ticket != nil && (ticket.Identity == "" || ticket.Identity == ticketIdentity(account))
}

func (s *OpenAIGatewayService) lookupCodexTicketLocked(account *Account, model string) *openAICodexTicket {
	if s == nil || account == nil || account.ID <= 0 {
		return nil
	}
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" {
		return nil
	}
	key := openAICodexTicketKey(account.ID, model)
	targetLen := openAICodexTicketTargetLength(account, s.openAICodexTicketConfig())
	now := time.Now()
	var mem *openAICodexTicket
	if raw, ok := s.openaiCodexTickets.Load(key); ok {
		mem, _ = raw.(*openAICodexTicket)
	}
	var extra *openAICodexTicket
	if account.Extra != nil {
		extra = parseOpenAICodexTicketFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
	}
	if targetLen == 780 {
		controls, _ := s.harvestControls(context.Background())
		protocol := controls.Transport
		if protocol == "" {
			protocol = "sse"
		}
		gateway := controls.TargetGateway
		if gateway == "" {
			gateway = "unified-88"
		}
		if mem != nil && (mem.Transport != protocol || !codex780GatewayAllowed(mem.Gateway, gateway)) {
			mem = nil
		}
		if extra != nil && (extra.Transport != protocol || !codex780GatewayAllowed(extra.Gateway, gateway)) {
			extra = nil
		}
	}
	if mem != nil && !ticketIdentityMatches(account, mem) {
		mem = nil
	}
	if extra != nil && !ticketIdentityMatches(account, extra) {
		extra = nil
	}
	if extra != nil && !extra.valid(now, targetLen) && extra.Standby.valid(now, targetLen) {
		extra = extra.Standby
	}
	// The cached tombstone wins over stale account snapshots after invalidation.
	if mem != nil && mem.Revoked {
		return mem
	}
	if mem != nil && !mem.valid(now, targetLen) && mem.Standby.valid(now, targetLen) {
		promoted := *mem.Standby
		s.openaiCodexTickets.Store(key, &promoted)
		return &promoted
	}
	if extra.valid(now, targetLen) && (mem == nil || extra.CapturedAt.After(mem.CapturedAt)) {
		s.openaiCodexTickets.Store(key, extra)
		return extra
	}
	if mem.valid(now, targetLen) {
		return mem
	}
	if extra != nil {
		s.openaiCodexTickets.Store(key, extra)
		return extra
	}
	if mem != nil {
		s.openaiCodexTickets.Delete(key)
	}
	return nil
}

func parseOpenAICodexTicketFromAny(accountID int64, model string, raw any) *openAICodexTicket {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var ticket openAICodexTicket
	if err := json.Unmarshal(b, &ticket); err != nil {
		return nil
	}
	ticket.AccountID = accountID
	if strings.TrimSpace(model) != "" {
		ticket.Model = model
	}
	ticket.State = strings.TrimSpace(ticket.State)
	if ticket.Length == 0 {
		ticket.Length = len(ticket.State)
	}
	if ticket.State == "" {
		return nil
	}
	return &ticket
}

func (s *OpenAIGatewayService) storeOpenAICodexTicket(ctx context.Context, account *Account, ticket *openAICodexTicket) error {
	if s == nil || account == nil || ticket == nil || account.ID <= 0 {
		return errors.New("invalid ticket store input")
	}
	incoming := *ticket
	standby := false
	s.openaiCodexTicketStateMu.Lock()
	defer s.openaiCodexTicketStateMu.Unlock()
	model := normalizeOpenAICodexTicketModel(ticket.Model)
	ticket.Identity = ticketIdentity(account)
	if current := s.lookupCodexTicketLocked(account, model); current.valid(time.Now(), openAICodexTicketTargetLength(account, s.openAICodexTicketConfig())) && current.State != ticket.State {
		copy := *current
		copy.Standby = ticket
		ticket = &copy
		standby = true
	}
	ticket.Model = model
	ticket.AccountID = account.ID
	s.openaiCodexTickets.Store(openAICodexTicketKey(account.ID, model), ticket)
	if s.accountRepo == nil {
		recordCodexHarvestTicketStore(account, &incoming, standby)
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
		openAICodexTicketExtraKey(model): ticket,
	}); err != nil {
		logger.L().Warn("openai_codex_ticket persist failed",
			zap.Int64("account_id", account.ID),
			zap.String("model", model),
			zap.Error(err),
		)
		return err
	}
	recordCodexHarvestTicketStore(account, &incoming, standby)
	return nil
}

func harvestTicketSessionID(ticket *openAICodexTicket) string {
	if ticket == nil || ticket.Length == 780 {
		return ""
	}
	return strings.TrimSpace(ticket.HarvestSessionID)
}

// harvestPinnedSessionForModel 返回即将注入的有效门票上的打票 session。
// 会话改写必须在 applyOpenAICodexTicket 之前就能判定，因为 Forward 的
// isolate/fingerprint 发生在出站头钉票之前。
func (s *OpenAIGatewayService) harvestPinnedSessionForModel(ctx context.Context, account *Account, model string) string {
	if s == nil || account == nil {
		return ""
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !s.openAICodexTicketEnabledContext(ctx) {
		return ""
	}
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" || !s.openAICodexTicketGatedModel(model) {
		return ""
	}
	ticket := s.lookupOpenAICodexTicket(account, model)
	if !ticket.valid(time.Now(), openAICodexTicketTargetLength(account, s.openAICodexTicketConfig())) {
		return ""
	}
	return harvestTicketSessionID(ticket)
}

func (s *OpenAIGatewayService) harvestPinsCodexIdentity(ctx context.Context, account *Account, model string) bool {
	return s.harvestPinnedSessionForModel(ctx, account, model) != ""
}

// applyOpenAICodexTicket 在出站请求上覆盖 x-codex-turn-state。
// 请求路径只注入已捕获的有效门票，不现场打票；无票则返回
// ErrOpenAICodexTicketUnavailable。打票由后台 harvester 完成。
func (s *OpenAIGatewayService) applyOpenAICodexTicket(ctx context.Context, account *Account, model string, h http.Header, transport ...string) error {
	if s == nil || h == nil || !isOpenAICodexTicketAccount(account, model) || !s.openAICodexTicketEnabledContext(ctx) {
		return nil
	}
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" || !s.openAICodexTicketGatedModel(model) {
		return nil
	}
	cfg := s.openAICodexTicketConfig()
	if !openAICodexSkipHarvest(account) && cfg.FailClosed && s.openAICodexTicketHarvestExcluded(account) {
		return denyOpenAITicket()
	}
	ticket := s.lookupOpenAICodexTicket(account, model)
	if ticket.valid(time.Now(), openAICodexTicketTargetLength(account, cfg)) {
		protocol := "sse"
		if (len(transport) > 0 && transport[0] == "websocket") || h.Get("OpenAI-Beta") == openAIWSBetaV1Value || h.Get("OpenAI-Beta") == openAIWSBetaV2Value || strings.EqualFold(h.Get("Upgrade"), "websocket") {
			protocol = "websocket"
		}
		if ticket.Transport != "" && ticket.Transport != protocol {
			if h.Get(openAICodexTurnStateHeader) == ticket.State {
				h.Del(openAICodexTurnStateHeader)
			}
			if cfg.FailClosed {
				return denyOpenAITicket()
			}
			return nil
		}

		h.Set(openAICodexTurnStateHeader, ticket.State)
		return nil
	}
	if openAICodexSkipHarvest(account) {
		return nil
	}
	if openAICodexSkipHarvest(account) {
		// Do not harvest, but the account stays usable. Own leftover tickets
		// were injected above; missing tickets fail-open instead of pausing.
		return nil
	}
	if !cfg.FailClosed {
		return nil
	}
	return denyOpenAITicket()
}

// openAICodexTicketOutboundModel 预测本请求真正出站的模型名，也就是
// applyOpenAICodexTicket 注入时读到的 body.model。
//
// 调度门控与注入必须按同一个模型名判定门票。普通请求下二者同源：Forward 的
// upstreamModel 与本函数都走 resolveOpenAIAccountUpstreamModelForRequest，且
// Forward 会把 body.model 改写成该值后才注入。但 /responses/compact 例外——
// Forward 会把出站模型进一步改写为 compact 映射或 gateway.openai_compact_model
// （默认非空），此时若门控仍按客户端原始模型判定，就会把「实际出站是非门控
// 模型、根本不需要票」的 compact 请求整片误拦成不可调度。
func (s *OpenAIGatewayService) openAICodexTicketOutboundModel(account *Account, requestedModel string, requireCompact bool) string {
	if account.IsExcelBPSEnabledForModel(requestedModel) {
		return account.GetMappedModel(requestedModel)
	}
	model := strings.TrimSpace(requestedModel)
	if account == nil || model == "" {
		return model
	}
	if !account.IsOpenAI() {
		return canonicalOpenAIAccountSchedulingModel(account, model)
	}
	_, upstreamModel := resolveOpenAIForwardMappedModels(account, model, requireCompact)
	if requireCompact {
		// 与 Forward 同序：compact 兜底模型优先于普通/compact 映射结果。
		if compactModel := strings.TrimSpace(s.resolveOpenAICompactFallbackModel(account, model)); compactModel != "" {
			upstreamModel = compactModel
		}
	}
	if upstreamModel = strings.TrimSpace(upstreamModel); upstreamModel != "" {
		return upstreamModel
	}
	return model
}

// outboundModel 必须是真正会发给上游的模型名（openAICodexTicketOutboundModel），
// 不是客户端原始模型：注入侧读的是出站 body.model，两侧口径必须一致。
func (s *OpenAIGatewayService) openAICodexTicketBlocksAccount(account *Account, outboundModel string) bool {
	if account.isPrismBrowserUpstreamModelEnabled(outboundModel) {
		return false
	}
	if s == nil || !isOpenAICodexTicketAccount(account, outboundModel) || !s.openAICodexTicketEnabled() {
		return false
	}
	cfg := s.openAICodexTicketConfig()
	if !cfg.FailClosed {
		return false
	}
	model := normalizeOpenAICodexTicketModel(outboundModel)
	if !s.openAICodexTicketGatedModel(model) {
		return false
	}
	if openAICodexSkipHarvest(account) {
		return false
	}
	if s.openAICodexTicketHarvestExcluded(account) {
		return true
	}
	ticket := s.lookupOpenAICodexTicket(account, model)
	return !ticket.valid(time.Now(), openAICodexTicketTargetLength(account, cfg))
}

func (s *OpenAIGatewayService) openAICodexTicketReadyForRequest(account *Account, requestedModel string, requireCompact bool) bool {
	if s == nil || account == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabled() {
		return false
	}
	outbound := s.openAICodexTicketOutboundModel(account, requestedModel, requireCompact)
	if account.isPrismBrowserUpstreamModelEnabled(outbound) || !isOpenAICodexTicketAccount(account, outbound) {
		return false
	}
	model := normalizeOpenAICodexTicketModel(outbound)
	if model == "" || !s.openAICodexTicketGatedModel(model) {
		return false
	}
	cfg := s.openAICodexTicketConfig()
	if cfg.FailClosed && !openAICodexSkipHarvest(account) && s.openAICodexTicketHarvestExcluded(account) {
		return false
	}
	ticket := s.lookupOpenAICodexTicket(account, model)
	return ticket.valid(time.Now(), openAICodexTicketTargetLength(account, cfg))
}

func (s *OpenAIGatewayService) openAICodexTicketFirstEnabled(ctx context.Context) bool {
	if s == nil || !s.openAICodexTicketEnabledContext(ctx) {
		return false
	}
	controls, _ := s.harvestControls(ctx)
	return controls.TicketFirstScheduling
}

// openAICodexTicketDeferredForRequest reports an account that would send a
// gated model through the native Codex route without a valid ticket. With
// ticket-first scheduling, ticket holders are tried first regardless of
// account priority; deferred accounts only take what they cannot serve. BPS
// and Prism routes never carry a ticket. Callers check the switch.
func (s *OpenAIGatewayService) openAICodexTicketDeferredForRequest(account *Account, requestedModel string, requireCompact bool) bool {
	if s == nil || account == nil {
		return false
	}
	outbound := s.openAICodexTicketOutboundModel(account, requestedModel, requireCompact)
	if account.isPrismBrowserUpstreamModelEnabled(outbound) || !isOpenAICodexTicketAccount(account, outbound) || !s.openAICodexTicketGatedModel(outbound) {
		return false
	}
	return !s.openAICodexTicketReadyForRequest(account, requestedModel, requireCompact)
}

// openAICodexTicketDeferredAccounts returns nil when no candidate is deferred,
// so callers can skip reordering entirely.
func (s *OpenAIGatewayService) openAICodexTicketDeferredAccounts(accounts []*Account, requestedModel string, requireCompact bool) map[int64]bool {
	if s == nil || len(accounts) < 2 || !s.openAICodexTicketFirstEnabled(context.Background()) {
		return nil
	}
	var deferred map[int64]bool
	preferred := false
	for _, account := range accounts {
		if account == nil {
			continue
		}
		if !s.openAICodexTicketDeferredForRequest(account, requestedModel, requireCompact) {
			preferred = true
			continue
		}
		if deferred == nil {
			deferred = make(map[int64]bool, len(accounts))
		}
		deferred[account.ID] = true
	}
	if !preferred {
		return nil
	}
	return deferred
}

func extraTruthy(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes":
			return true
		}
	case float64:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	}
	return false
}

func openAICodexSkipHarvest(account *Account) bool {
	if account == nil || account.Extra == nil {
		return false
	}
	return extraTruthy(account.Extra[OpenAICodexSkipHarvestExtraKey])
}

func (s *OpenAIGatewayService) holdCodexTicketChat(account *Account) func() {
	if s == nil || account == nil || account.ID <= 0 {
		return func() {}
	}
	raw, _ := s.openaiCodexTicketChatHold.LoadOrStore(account.ID, new(int64))
	ctr, _ := raw.(*int64)
	if ctr == nil {
		return func() {}
	}
	atomic.AddInt64(ctr, 1)
	var once sync.Once
	return func() {
		once.Do(func() {
			atomic.AddInt64(ctr, -1)
		})
	}
}

func (s *OpenAIGatewayService) codexTicketChatHeld(accountID int64) bool {
	if s == nil || accountID <= 0 {
		return false
	}
	raw, ok := s.openaiCodexTicketChatHold.Load(accountID)
	if !ok {
		return false
	}
	ctr, _ := raw.(*int64)
	return ctr != nil && atomic.LoadInt64(ctr) > 0
}

func (s *OpenAIGatewayService) manualHarvestLiveModels(account *Account, models []string) map[string]struct{} {
	got := map[string]struct{}{}
	if s == nil || account == nil {
		return got
	}
	cfg := s.openAICodexTicketConfig()
	now := time.Now()
	target := openAICodexTicketTargetLength(account, cfg)
	for _, model := range models {
		if s.lookupOpenAICodexTicket(account, model).valid(now, target) {
			got[model] = struct{}{}
		}
	}
	return got
}

func (s *OpenAIGatewayService) markManualHarvestLiveModels(account *Account, models []string, got map[string]struct{}) {
	if got == nil {
		return
	}
	for model := range s.manualHarvestLiveModels(account, models) {
		got[model] = struct{}{}
	}
}

func (s *OpenAIGatewayService) accountHasLiveCodexTicket(account *Account) bool {
	if s == nil || account == nil {
		return false
	}
	return len(s.manualHarvestLiveModels(account, s.openAICodexTicketConfig().Models)) > 0
}

// Harvest-excluded is harvest-scope only: accounts outside selected groups
// are not probed, and fail-closed routing will not spend their leftover
// tickets. Per-account skip_harvest is different — it only skips probing;
// lookup stays per-account, so a 5x cannot spend a 20x Extra ticket.
func (s *OpenAIGatewayService) openAICodexTicketHarvestExcluded(account *Account) bool {
	if s == nil || s.settingService == nil || account == nil {
		return false
	}
	scope, err := s.settingService.GetCodexTicketHarvestScope(context.Background())
	if err != nil {
		return true
	}
	if scope.Mode != "selected" {
		return false
	}
	return !scope.includes(account)
}

// Sticky sessions bypass ticket-first ordering and account priority. With
// ticket-first scheduling, a session bound to an account without a ticket
// would never reach a ticket holder; independently, a leftover 292 on a
// low-priority account (for example a 5x that is no longer harvested) would
// pin traffic even after a higher-priority 20x harvests.
func (s *OpenAIGatewayService) openAICodexTicketShouldYieldStickyTo(sticky *Account, candidates []*Account, requestedModel string, requireCompact bool, excludedIDs map[int64]struct{}) bool {
	if s == nil || sticky == nil || !s.openAICodexTicketEnabled() {
		return false
	}
	outbound := s.openAICodexTicketOutboundModel(sticky, requestedModel, requireCompact)
	if !s.openAICodexTicketGatedModel(outbound) {
		return false
	}
	stickyDeferred := s.openAICodexTicketFirstEnabled(context.Background()) && s.openAICodexTicketDeferredForRequest(sticky, requestedModel, requireCompact)
	stickyPriority := openAIAccountSchedulingPriority(sticky)
	for _, account := range candidates {
		if account == nil || account.ID == sticky.ID {
			continue
		}
		if excludedIDs != nil {
			if _, skipped := excludedIDs[account.ID]; skipped {
				continue
			}
		}
		if !stickyDeferred && openAIAccountSchedulingPriority(account) >= stickyPriority {
			continue
		}
		if s.isOpenAIAccountRequestRuntimeBlocked(account, requestedModel, requireCompact) {
			continue
		}
		if !s.openAICodexTicketReadyForRequest(account, requestedModel, requireCompact) {
			continue
		}
		return true
	}
	return false
}

func (s *OpenAIGatewayService) openAICodexTicketShouldYieldSticky(ctx context.Context, sticky *Account, groupID *int64, platform, requestedModel string, requireCompact bool, excludedIDs map[int64]struct{}) bool {
	if s == nil || sticky == nil || !s.openAICodexTicketEnabled() {
		return false
	}
	accounts, err := s.listSchedulableAccountsForRequest(ctx, groupID, platform, requestedModel, requireCompact, excludedIDs)
	if err != nil || len(accounts) == 0 {
		return false
	}
	candidates := make([]*Account, 0, len(accounts))
	for i := range accounts {
		candidates = append(candidates, &accounts[i])
	}
	return s.openAICodexTicketShouldYieldStickyTo(sticky, candidates, requestedModel, requireCompact, excludedIDs)
}

func (s *OpenAIGatewayService) fireOpenAICodexTicketProbe(ctx context.Context, account *Account, token, model, proxyURL string, attemptTimeout time.Duration) (state string, status int, err error) {
	// A fresh probe must start a fresh upstream session. The resulting ticket
	// carries that session for later conversation egress.
	result := s.executeCodexHarvestProbe(ctx, account, token, model, proxyURL, attemptTimeout, nil, "")
	return result.State, result.Status, result.Err
}

func (s *OpenAIGatewayService) ticketProbeCoolingDown(accountID int64, model string, now time.Time) bool {
	if s == nil {
		return false
	}
	key := openAICodexTicketKey(accountID, model)
	value, _ := s.openaiCodexTicketProbeCooldown.Load(key)
	until, ok := value.(time.Time)
	if !ok || !until.After(now) {
		s.openaiCodexTicketProbeCooldown.Delete(key)
		return false
	}
	return true
}

func jsonString(v string) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(b)
}

func applyOpenAICodexTicketHarvestIdentity(h http.Header, model string) {
	ensureCodexIdentityHeaders(h)
	enforceCodexIdentityHeaders(h)
	version := strings.TrimSpace(h.Get("version"))
	if needsOpenAICodexAstraVersion(model) && (version == "" || CompareVersions(version, openAICodexAstraMinVersion) < 0) {
		h.Set("version", openAICodexAstraMinVersion)
		h.Set("user-agent", buildCodexCLIUserAgent(openAICodexAstraMinVersion))
		h.Set("originator", openai.CodexDefaultOriginator)
	}
}

func needsOpenAICodexAstraVersion(model string) bool {
	m := strings.ToLower(normalizeOpenAICodexTicketModel(model))
	return strings.Contains(m, "gpt-6") || strings.Contains(m, "astra")
}

func (s *OpenAIGatewayService) StartOpenAICodexTicketHarvester() {
	if s == nil || !s.cfg.RunsBackgroundJobs() {
		return
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	defer s.openaiCodexTicketLifecycleMu.Unlock()
	if s.openaiCodexTicketStopped || s.openaiCodexTicketDone != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.openaiCodexTicketCancel = cancel
	s.openaiCodexTicketDone = done
	go func() {
		defer close(done)
		s.openAICodexTicketHarvestLoop(ctx)
	}()
	logger.L().Info("openai_codex_ticket harvester started",
		zap.Int("ttl_seconds", s.openAICodexTicketConfig().TTLSeconds),
		zap.Int("target_length", s.openAICodexTicketConfig().TargetLength),
		zap.Strings("models", s.openAICodexTicketConfig().Models),
	)
}

func (s *OpenAIGatewayService) StopOpenAICodexTicketHarvester() {
	if s == nil {
		return
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	s.openaiCodexTicketStopped = true
	cancel, done := s.openaiCodexTicketCancel, s.openaiCodexTicketDone
	s.openaiCodexTicketLifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// Bound admin-triggered rounds without delaying a wake until the normal probe
// interval. This does not change per-account cooldowns or probe limits.
const openAICodexTicketWakeMinInterval = time.Second

func (s *OpenAIGatewayService) openAICodexTicketHarvestLoop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	var controlWake <-chan struct{}
	if s.codexHarvest != nil {
		controlWake = s.codexHarvest.wake
	}
	settingsWake := s.settingService.codexHarvestWakeups()
	var lastRound time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-controlWake:
			s.resetHarvestWakeTimer(timer, lastRound)
		case <-settingsWake:
			s.resetHarvestWakeTimer(timer, lastRound)
		case <-timer.C:
			s.refreshOpenAICodexTickets(ctx)
			lastRound = time.Now()
			s.armHarvestIntervalTimer(ctx, timer, lastRound)
		}
	}
}

func (s *OpenAIGatewayService) resetHarvestWakeTimer(timer *time.Timer, lastRound time.Time) {
	delay := time.Until(lastRound.Add(openAICodexTicketWakeMinInterval))
	if delay < 0 {
		delay = 0
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(delay)
	next := time.Now().Add(delay)
	if s.codexHarvest != nil {
		s.codexHarvest.setRuntime(func(r *CodexHarvestRuntime) { r.NextRoundAt = &next })
	}
}

func (s *OpenAIGatewayService) armHarvestIntervalTimer(ctx context.Context, timer *time.Timer, lastRound time.Time) {
	controls, _ := s.harvestControls(ctx)
	next := lastRound.Add(time.Duration(controls.Speed.RoundIntervalSeconds) * time.Second)
	if s.codexHarvest != nil {
		s.codexHarvest.setRuntime(func(r *CodexHarvestRuntime) { r.NextRoundAt = &next })
	}
	delay := time.Until(next)
	if delay < 0 {
		delay = 0
	}
	timer.Reset(delay)
}

// refreshOpenAICodexTickets preserves priority tiers and fair cursors while
// charging the shared round budget only when an upstream request is sent.
func (s *OpenAIGatewayService) refreshOpenAICodexTickets(ctx context.Context) {
	if s == nil || s.accountRepo == nil || ctx.Err() != nil || !s.openAICodexTicketEnabledContext(ctx) {
		return
	}
	if !s.codexHarvestRoundActive.CompareAndSwap(false, true) {
		return
	}
	defer s.codexHarvestRoundActive.Store(false)
	controls, configured := s.harvestControls(ctx)
	legacyParallel := !configured && !controls.NodeMemoryEnabled
	round := &codexHarvestRound{limit: controls.Speed.MaxRequestsPerRound}
	ctx = context.WithValue(ctx, codexHarvestRoundKey{}, round)
	if s.codexHarvest != nil {
		s.codexHarvest.setRuntime(func(r *CodexHarvestRuntime) {
			r.Running = true
			r.RequestsUsed = 0
			r.RequestBudget = round.limit
			r.NextRoundAt = nil
		})
		defer s.codexHarvest.setRuntime(func(r *CodexHarvestRuntime) { r.Running = false; r.CurrentNode = "" })
	}
	observeCodexHarvestProxy(ctx, s.openAICodexTicketHarvestProxySetting(ctx))
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		logger.L().Warn("openai_codex_ticket list accounts failed", zap.Error(err))
		return
	}
	cfg := s.harvestTicketConfig(ctx)
	now := time.Now()
	refreshBefore := time.Duration(cfg.RefreshBeforeSeconds) * time.Second
	if s.settingService.GetCodexTicketStrategy(ctx) == "fixed" {
		refreshBefore = 0
	}
	scope, err := s.settingService.GetCodexTicketHarvestScope(ctx)
	if err != nil {
		logger.L().Warn("openai_codex_ticket harvest scope unavailable; skipping round", zap.Error(err))
		return
	}
	// Independent circular queues prevent a cursor in the deferred tail from
	// bypassing schedulable accounts at the start of the next round.
	tiers := map[codexHarvestTier][]Account{}
	seen := make(map[int64]bool, len(accounts))
	for _, account := range accounts {
		if seen[account.ID] || openAICodexSkipHarvest(&account) || !scope.includes(&account) || !scope.allowsAccount(&account) || !isOpenAICodexTicketAccount(&account) {
			continue
		}
		seen[account.ID] = true
		tier := scope.tier(&account)
		tiers[tier] = append(tiers[tier], account)
	}
	keys := make([]codexHarvestTier, 0, len(tiers))
	for key := range tiers {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Schedulable != keys[j].Schedulable {
			return keys[i].Schedulable
		}
		if keys[i].Priority != keys[j].Priority {
			return keys[i].Priority < keys[j].Priority
		}
		return keys[i].AccountPriority < keys[j].AccountPriority
	})
	// Remove obsolete buckets when membership/priorities change.
	s.openaiCodexTicketCursors.Range(func(key, _ any) bool {
		tier, ok := key.(codexHarvestTier)
		if !ok {
			s.openaiCodexTicketCursors.Delete(key)
			return true
		}
		if _, exists := tiers[tier]; !exists {
			s.openaiCodexTicketCursors.Delete(key)
		}
		return true
	})
	counts := [2]int{}
	for _, tier := range keys {
		pool := tiers[tier]
		// The repository orders by global priority only. Stabilize equal-priority
		// rows so database tie ordering cannot defeat round-robin fairness.
		sort.Slice(pool, func(i, j int) bool { return pool[i].ID < pool[j].ID })
		if ctx.Err() != nil || round.Used() >= round.limit {
			break
		}
		total := len(pool) * len(cfg.Models)
		if total == 0 {
			continue
		}
		stored, _ := s.openaiCodexTicketCursors.LoadOrStore(tier, &atomic.Uint64{})
		cursor, ok := stored.(*atomic.Uint64)
		if !ok || cursor == nil {
			cursor = &atomic.Uint64{}
			s.openaiCodexTicketCursors.Store(tier, cursor)
		}
		start := int(cursor.Load() % uint64(total))
		before, scheduled := round.Used(), 0
		var wg sync.WaitGroup
		for offset := 0; offset < total && round.Used() < round.limit && ctx.Err() == nil; offset++ {
			if legacyParallel && scheduled >= round.limit-before {
				break
			}
			index := (start + offset) % total
			cursor.Store(uint64((index + 1) % total))
			account := pool[index/len(cfg.Models)]
			model := normalizeOpenAICodexTicketModel(cfg.Models[index%len(cfg.Models)])
			if model == "" || s.ticketProbeCoolingDown(account.ID, model, now) {
				continue
			}
			ticket := s.lookupOpenAICodexTicket(&account, model)
			if ticket.valid(now, openAICodexTicketTargetLength(&account, cfg)) && !ticket.needsRefresh(now, refreshBefore) {
				continue
			}
			if ticket != nil && ticket.Standby.valid(now, openAICodexTicketTargetLength(&account, cfg)) && !ticket.Standby.needsRefresh(now, refreshBefore) {
				continue
			}
			acc := account
			acc.Extra = maps.Clone(account.Extra)
			acc.Credentials = maps.Clone(account.Credentials)
			if legacyParallel {
				scheduled++
				wg.Add(1)
				go func(acc Account, model string) {
					defer wg.Done()
					s.probeOnceOpenAICodexTicket(ctx, &acc, model)
				}(acc, model)
			} else {
				s.probeOnceOpenAICodexTicket(ctx, &acc, model)
			}
		}
		wg.Wait()
		if tier.Schedulable {
			counts[0] += round.Used() - before
		} else {
			counts[1] += round.Used() - before
		}
	}
	if round.Used() > 0 {
		logger.L().Info("openai_codex_ticket probe cycle", zap.Int("probed", round.Used()),
			zap.Int("schedulable_probed", counts[0]), zap.Int("deferred_probed", counts[1]),
			zap.String("scope", scope.Mode), zap.Int("selected_groups", len(scope.GroupIDs)))
	}
}

// IsOpenAICodexTicketExtraKey identifies server-managed ticket material.
func IsOpenAICodexTicketExtraKey(key string) bool {
	return strings.HasPrefix(key, openAICodexTicketExtraKeyPrefix)
}

// MergeOpenAICodexTicketExtra preserves only persisted tickets, never summaries or
// blobs supplied by an account edit. The repository repeats this under the row
// lock so a concurrent harvest cannot be overwritten by a stale admin snapshot.
func MergeOpenAICodexTicketExtra(extra, current map[string]any) map[string]any {
	result := maps.Clone(extra)
	for key := range result {
		if IsOpenAICodexTicketExtraKey(key) || key == ExcelBPSCredentialStateExtraKey {
			delete(result, key)
		}
	}
	for key, value := range current {
		if IsOpenAICodexTicketExtraKey(key) || key == ExcelBPSCredentialStateExtraKey {
			if result == nil {
				result = make(map[string]any)
			}
			result[key] = value
		}
	}
	return result
}

// ValidateOpenAICodexTicketHarvestProxyURL validates only syntax, without making
// a network request or including credentials in validation errors.
func ValidateOpenAICodexTicketHarvestProxyURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// The IP 管理 pool sentinel is not a dialable exit; it is resolved per attempt.
	if raw == OpenAICodexTicketHarvestIPPoolURL {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("harvest proxy must be an HTTP(S) or SOCKS5(h) URL with a host and no path, query or fragment")
	}
	switch parsed.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return errors.New("harvest proxy scheme must be http, https, socks5 or socks5h")
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("harvest proxy port must be between 1 and 65535")
		}
	}
	return nil
}

// MaskProxyURL never returns a stored proxy password, even for invalid legacy data.
func MaskProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || ValidateOpenAICodexTicketHarvestProxyURL(raw) != nil {
		return ""
	}
	// The pool sentinel carries no credentials; surface it verbatim so the panel
	// can restore the "IP 管理 pool" selection after save.
	if raw == OpenAICodexTicketHarvestIPPoolURL {
		return raw
	}
	parsed, _ := url.Parse(raw)
	if parsed.User != nil {
		if _, ok := parsed.User.Password(); ok {
			parsed.User = url.UserPassword(parsed.User.Username(), "***")
		}
	}
	return parsed.String()
}

// IsMaskedProxyURL recognizes the exact password placeholder emitted by the API.
func IsMaskedProxyURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return false
	}
	password, ok := parsed.User.Password()
	return ok && password == "***"
}

// Credential shadows do not own tickets. Keep their existing forwarding policy
// instead of imposing a gate for a key the harvester never populates.
func isOpenAICodexTicketAccount(account *Account, upstreamModels ...string) bool {
	if account == nil || !account.IsOpenAIOAuthLike() || account.IsShadow() || account.isExcelBPSAllModelsEnabled() {
		return false
	}
	return len(upstreamModels) == 0 || !account.isExcelBPSUpstreamModelEnabled(upstreamModels[0])
}

// IsOpenAICodexTicketPrivateExtraKey also covers the retired account-level proxy
// override, whose credentials may remain in older account records.
func IsOpenAICodexTicketPrivateExtraKey(key string) bool {
	return IsOpenAICodexTicketExtraKey(key) || key == "codex_harvest_proxy_url"
}

// RedactOpenAICodexTicketExtra strips ephemeral ticket material from exports
// without changing the source account or unrelated backup fields.
func RedactOpenAICodexTicketExtra(extra map[string]any) map[string]any {
	redacted := maps.Clone(extra)
	for key := range redacted {
		if IsOpenAICodexTicketPrivateExtraKey(key) {
			delete(redacted, key)
		}
	}
	return redacted
}
