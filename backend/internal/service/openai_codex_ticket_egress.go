package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/mihomo"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var boundCodexTicketForeignIdentityHeaders = []string{
	"session-id",
	"conversation_id",
	"installation_id",
	"x-codex-installation-id",
	"thread_id",
	"thread-id",
	"turn_id",
	"turn-id",
	"window_id",
	"x-codex-window-id",
	"x-client-request-id",
	"x-codex-turn-metadata",
}

// Harvest probes never send these. Conversation / WS inject them after the
// probe identity is already locked into the 292.
var boundCodexTicketHarvestStrippedHeaders = []string{
	"x-codex-beta-features",
	openAICodexRoutingHintHeader,
	"accept-language",
	responsesLiteHeaderKey,
}

var boundCodexTicketHarvestStrippedBodyKeys = []string{
	"prompt_cache_key",
	"client_metadata",
	"device_id",
}

type codexTicketEgressBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *codexTicketEgressBody) Close() error {
	err := b.ReadCloser.Close()
	if b.release != nil {
		b.once.Do(b.release)
	}
	return err
}

func restoreBoundCodexTicketHarvestIdentity(h http.Header, ticket *openAICodexTicket) {
	if h == nil || ticket == nil {
		return
	}
	if ticket.Length == 780 {
		h.Del(responsesLiteHeaderKey)
		if codexTicketCookiesFresh(ticket, time.Now()) {
			h.Set("Cookie", strings.Join(ticket.HarvestCookies, "; "))
		} else {
			h.Del("Cookie")
		}
		return
	}
	prevBeta := strings.TrimSpace(h.Get("OpenAI-Beta"))
	if session := harvestTicketSessionID(ticket); session != "" {
		h.Set("session_id", session)
	}
	if codexTicketCookiesFresh(ticket, time.Now()) {
		h.Set("Cookie", strings.Join(ticket.HarvestCookies, "; "))
	} else {
		h.Del("Cookie")
	}
	for _, key := range boundCodexTicketForeignIdentityHeaders {
		h.Del(key)
	}
	for _, key := range boundCodexTicketHarvestStrippedHeaders {
		h.Del(key)
	}
	applyOpenAICodexTicketHarvestIdentity(h, ticket.Model)
	for _, key := range boundCodexTicketHarvestStrippedHeaders {
		h.Del(key)
	}
	if ticket.HarvestLite {
		h.Set(responsesLiteHeaderKey, "true")
	}
	if prevBeta == openAIWSBetaV1Value || prevBeta == openAIWSBetaV2Value {
		h.Set("OpenAI-Beta", prevBeta)
	}
}

func (s *OpenAIGatewayService) restoreBoundCodexTicketHarvestIdentity(ctx context.Context, h http.Header, account *Account) {
	if s == nil {
		return
	}
	restoreBoundCodexTicketHarvestIdentity(h, s.boundCodexTicketFromHeader(ctx, h, account))
}

// Harvest probes send session_id on the header only: no prompt_cache_key and
// no client_metadata. Conversation traffic must match that shape. Rewriting
// those fields onto the harvest session still presents a second identity
// (installation/thread/turn/cache) that the issuing probe never had.
func pinBoundCodexTicketHarvestIdentityMaps(body map[string]any, ticket *openAICodexTicket) bool {
	if body == nil || harvestTicketSessionID(ticket) == "" {
		return false
	}
	changed := false
	for _, key := range boundCodexTicketHarvestStrippedBodyKeys {
		if _, ok := body[key]; !ok {
			continue
		}
		delete(body, key)
		changed = true
	}
	return changed
}

func pinBoundCodexTicketHarvestIdentityBody(body []byte, ticket *openAICodexTicket) ([]byte, bool, error) {
	if len(body) == 0 || harvestTicketSessionID(ticket) == "" {
		return body, false, nil
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return body, false, nil
	}
	next := body
	changed := false
	for _, key := range boundCodexTicketHarvestStrippedBodyKeys {
		if !gjson.GetBytes(next, key).Exists() {
			continue
		}
		rewritten, err := sjson.DeleteBytes(next, key)
		if err != nil {
			return body, false, fmt.Errorf("strip harvest identity %s: %w", key, err)
		}
		next = rewritten
		changed = true
	}
	return next, changed, nil
}

func replaceHTTPRequestJSONBody(req *http.Request, body []byte) {
	if req == nil {
		return
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
}

func snapshotHTTPRequestBody(req *http.Request) ([]byte, error) {
	if req == nil {
		return nil, nil
	}
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		defer func() { _ = body.Close() }()
		return io.ReadAll(body)
	}
	if req.Body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	return raw, err
}

func pinBoundCodexTicketHarvestIdentityRequestBody(req *http.Request, ticket *openAICodexTicket) {
	if req == nil || harvestTicketSessionID(ticket) == "" {
		return
	}
	raw, err := snapshotHTTPRequestBody(req)
	if err != nil {
		if raw != nil {
			replaceHTTPRequestJSONBody(req, raw)
		}
		return
	}
	next := raw
	if pinned, changed, pinErr := pinBoundCodexTicketHarvestIdentityBody(raw, ticket); pinErr == nil && changed {
		next = pinned
	}
	replaceHTTPRequestJSONBody(req, next)
}

func (s *OpenAIGatewayService) pinBoundCodexTicketHarvestIdentity(req *http.Request, account *Account) {
	if s == nil || req == nil {
		return
	}
	ticket := s.boundCodexTicket(req, account)
	restoreBoundCodexTicketHarvestIdentity(req.Header, ticket)
	pinBoundCodexTicketHarvestIdentityRequestBody(req, ticket)
}

func (s *OpenAIGatewayService) pinHarvestIdentityMapsForModel(ctx context.Context, account *Account, model string, body map[string]any, clientSessionIDs ...string) bool {
	session := s.harvestPinnedSessionForModel(ctx, account, model)
	if session == "" {
		return false
	}
	return pinBoundCodexTicketHarvestIdentityMaps(body, &openAICodexTicket{HarvestSessionID: session})
}

func (s *OpenAIGatewayService) pinHarvestIdentityBodyForModel(ctx context.Context, account *Account, model string, body []byte, clientSessionIDs ...string) ([]byte, error) {
	session := s.harvestPinnedSessionForModel(ctx, account, model)
	if session == "" {
		return body, nil
	}
	next, _, err := pinBoundCodexTicketHarvestIdentityBody(body, &openAICodexTicket{HarvestSessionID: session})
	if err != nil {
		return body, err
	}
	return next, nil
}

func (s *OpenAIGatewayService) applyCodexAccountIdentityOrHarvestPinMap(ctx context.Context, account, identityAccount *Account, apiKeyID int64, model string, body map[string]any, clientSessionIDs ...string) bool {
	if s.pinHarvestIdentityMapsForModel(ctx, account, model, body) {
		return true
	}
	return applyCodexAccountIdentityClientMetadataMap(body, identityAccount, apiKeyID, clientSessionIDs...)
}

func (s *OpenAIGatewayService) applyCodexAccountIdentityOrHarvestPinRaw(ctx context.Context, account, identityAccount *Account, apiKeyID int64, model string, body []byte, clientSessionIDs ...string) ([]byte, error) {
	if session := s.harvestPinnedSessionForModel(ctx, account, model); session != "" {
		next, _, err := pinBoundCodexTicketHarvestIdentityBody(body, &openAICodexTicket{HarvestSessionID: session})
		if err != nil {
			return body, err
		}
		return next, nil
	}
	next, _, err := applyCodexAccountIdentityClientMetadataRaw(body, identityAccount, apiKeyID, clientSessionIDs...)
	if err != nil {
		return body, err
	}
	return next, nil
}

func openAIAccountProxyURL(account *Account) string {
	if account != nil && account.ProxyID != nil && account.Proxy != nil {
		return account.Proxy.URL()
	}
	return ""
}

func (s *OpenAIGatewayService) stickBoundCodexTicketRequest(req *http.Request, account *Account) *http.Request {
	if req == nil {
		return req
	}
	ticket := s.boundCodexTicket(req, account)
	if ticket == nil {
		return req
	}
	boundAstra := ticket.Length == 780 && s.UnifiedAstraTicketsEnabled(req.Context())
	ctx := req.Context()
	if boundAstra {
		ctx = context.WithValue(ctx, boundAstraTicketContextKey{}, true)
	}
	req = req.WithContext(WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileOpenAIHarvest))
	req.Close = true
	restoreBoundCodexTicketHarvestIdentity(req.Header, ticket)
	pinBoundCodexTicketHarvestIdentityRequestBody(req, ticket)
	return req
}

func (s *OpenAIGatewayService) loadCodexTicketDirectedSidecar(ctx context.Context, proxyURL string) (*mihomo.DirectedSidecar, error) {
	dataDir := os.Getenv("DATA_DIR")
	seen := map[string]bool{}
	var last error
	for _, candidate := range []string{proxyURL, s.openAICodexTicketHarvestFixedProxyURL(ctx)} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		sidecar, err := mihomo.LoadDirectedSidecar(dataDir, candidate)
		if err == nil {
			return sidecar, nil
		}
		last = err
	}
	if last != nil {
		return nil, last
	}
	return nil, ErrOpenAICodexTicketUnavailable
}

func (s *OpenAIGatewayService) codexTicketPinsEgress(req *http.Request, account *Account) bool {
	if req == nil {
		return false
	}
	return s.codexTicketPinsEgressFromHeader(req.Context(), req.Header, account)
}

func (s *OpenAIGatewayService) codexTicketPinsEgressFromHeader(ctx context.Context, h http.Header, account *Account) bool {
	ticket := s.boundCodexTicketFromHeader(ctx, h, account)
	if ticket == nil {
		return false
	}
	return strings.TrimSpace(ticket.HarvestProxyURL) != "" ||
		strings.TrimSpace(ticket.HarvestNodeID) != "" ||
		strings.TrimSpace(ticket.HarvestNodeName) != "" ||
		s.openAICodexTicketHarvestFixedProxyURL(ctx) != ""
}

func (s *OpenAIGatewayService) pinCodexTicketEgress(req *http.Request, account *Account, proxyURL string) (string, func(), error) {
	if req == nil {
		return proxyURL, func() {}, nil
	}
	return s.pinCodexTicketEgressFromHeader(req.Context(), req.Header, account, proxyURL)
}

func (s *OpenAIGatewayService) pinCodexTicketWSAcquire(ctx context.Context, headers http.Header, account *Account) (string, func(), error) {
	s.restoreBoundCodexTicketHarvestIdentity(ctx, headers, account)
	return s.pinCodexTicketEgressFromHeader(ctx, headers, account, openAIAccountProxyURL(account))
}

func (s *OpenAIGatewayService) pinCodexTicketEgressFromHeader(ctx context.Context, h http.Header, account *Account, proxyURL string) (string, func(), error) {
	noop := func() {}
	ticket := s.boundCodexTicketFromHeader(ctx, h, account)
	if ticket == nil {
		return proxyURL, noop, nil
	}
	return s.pinStoredCodexTicketEgress(ctx, ticket, proxyURL)
}

// A single complete ticket determines the egress for self and reuse acquisition.
func (s *OpenAIGatewayService) pinStoredCodexTicketEgress(ctx context.Context, ticket *openAICodexTicket, proxyURL string) (string, func(), error) {
	noop := func() {}
	pinned := strings.TrimSpace(ticket.HarvestProxyURL)
	if pinned == "" {
		pinned = s.openAICodexTicketHarvestFixedProxyURL(ctx)
	}
	if pinned == "" {
		return proxyURL, noop, nil
	}
	if strings.TrimSpace(ticket.HarvestNodeID) == "" && strings.TrimSpace(ticket.HarvestNodeName) == "" {
		return pinned, noop, nil
	}
	if ticket.HarvestNodeProvider == "managed" {
		proxy, release, err := mihomo.PinNode(ctx, ticket.HarvestNodeID)
		if err != nil {
			return "", noop, ErrOpenAICodexTicketUnavailable
		}
		return proxy, release, nil
	}
	sidecar, err := s.loadCodexTicketDirectedSidecar(ctx, pinned)
	if err != nil {
		return "", noop, ErrOpenAICodexTicketUnavailable
	}
	node, ok := sidecar.Lookup(ctx, ticket.HarvestNodeID, ticket.HarvestNodeName)
	if !ok {
		return "", noop, ErrOpenAICodexTicketUnavailable
	}
	release, err := sidecar.Acquire(ctx, node)
	if err != nil {
		return "", noop, ErrOpenAICodexTicketUnavailable
	}

	return sidecar.ProxyURL, release, nil
}

func attachCodexTicketEgressRelease(resp *http.Response, err error, release func()) (*http.Response, error) {
	if release == nil {
		return resp, err
	}
	if err != nil || resp == nil || resp.Body == nil {
		release()
		return resp, err
	}
	resp.Body = &codexTicketEgressBody{ReadCloser: resp.Body, release: release}
	return resp, err
}
