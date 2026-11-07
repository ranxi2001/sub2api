package service

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// maxPersistedSessionIDLength bounds the persisted client session identifier to the
// usage_logs.session_id column width (VARCHAR(255)). Longer values are rejected so
// distinct identifiers can never alias through truncation.
const maxPersistedSessionIDLength = 255

// clientSessionIDHeaders extends the OpenAI-compatible sticky-session signals with
// native protocol identifiers that are safe to persist but must not alter OpenAI
// scheduling behavior.
var clientSessionIDHeaders = append(
	append([]string(nil), explicitOpenAIHeaderSessionNames...),
	claudeCodeSessionHeader,
)

const (
	openAIClientSessionKindThread  = "thread"
	openAIClientSessionKindSession = "session"
)

var openAIThreadIdentityHeaders = []string{
	"conversation_id",
	"thread_id",
	"thread-id",
	codeBuddyConversationHeader,
}

var openAISessionIdentityHeaders = []string{
	"session_id",
	"session-id",
	openCodeSessionIDHeader,
	openCodeNativeSessionHeader,
}

type openAIClientSessionIdentity struct {
	kind  string
	value string
}

type OpenAIClientSessionIdentityStatus string

const (
	OpenAIClientSessionIdentityResolved OpenAIClientSessionIdentityStatus = "resolved"
	OpenAIClientSessionIdentityMissing  OpenAIClientSessionIdentityStatus = "missing"
	OpenAIClientSessionIdentityConflict OpenAIClientSessionIdentityStatus = "conflict"
	OpenAIClientSessionIdentityInvalid  OpenAIClientSessionIdentityStatus = "invalid"
)

const (
	OpenAIClientSessionIdentitySourceNone       = "none"
	OpenAIClientSessionIdentitySourceHeader     = "header"
	OpenAIClientSessionIdentitySourceBody       = "body"
	OpenAIClientSessionIdentitySourceHeaderBody = "header_body"
	OpenAIClientSessionIdentitySourceConnection = "connection"
)

// OpenAIClientSessionIdentityMetadata contains only non-sensitive resolution
// metadata. It is safe to use in structured logs and counters because it never
// exposes the client-provided identity value.
type OpenAIClientSessionIdentityMetadata struct {
	Status OpenAIClientSessionIdentityStatus
	Kind   string
	Source string
}

type openAIClientSessionIdentityResolution struct {
	metadata OpenAIClientSessionIdentityMetadata
	identity openAIClientSessionIdentity
}

type openAIIdentityValue struct {
	value  string
	status OpenAIClientSessionIdentityStatus
}

// ClaudeCodeSessionIDFromHeader returns the stable Claude Code conversation
// identifier carried by X-Claude-Code-Session-Id. It is intentionally exposed
// separately from ExtractClientSessionID: callers that use it for routing must
// make that scope explicit rather than accidentally changing every protocol's
// session semantics.
func ClaudeCodeSessionIDFromHeader(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return sanitizeSessionID(c.GetHeader(claudeCodeSessionHeader))
}

// ExtractClientSessionID resolves the explicit client-provided session identifier from
// request headers for usage-log correlation and returns it sanitized. It is
// protocol-agnostic and shared by every gateway handler so all supported protocols
// record session_id through one seam. Returns "" when no valid identifier is present.
//
// This value feeds only usage_logs.session_id persistence. It does NOT affect sticky
// routing, account selection, request_id semantics, or upstream prompt caching, which
// keep their own (intentionally broader) session-signal resolution.
func ExtractClientSessionID(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	for _, header := range clientSessionIDHeaders {
		if sessionID := sanitizeSessionID(c.GetHeader(header)); sessionID != "" {
			return sessionID
		}
	}
	if isGrokRequestContext(c) {
		if sessionID := sanitizeSessionID(c.GetHeader(grokConversationIDHeader)); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

// ExtractOpenAIClientSessionID resolves the explicit OpenAI conversation
// identity from request headers and client_metadata. Thread identities take
// precedence over session identities. A conflicting header/body value of the
// same kind is rejected instead of silently choosing one, so usage correlation
// and Cyber blocking cannot disagree about which conversation was identified.
// When no OpenAI identity is present, the legacy Claude Code session header is
// retained for usage correlation. It is not used by Cyber block-key derivation.
//
// prompt_cache_key and X-Session-Affinity are intentionally excluded: they are
// scheduling/cache hints, not reliable conversation identities.
func ExtractOpenAIClientSessionID(c *gin.Context, body []byte) string {
	resolution := resolveOpenAIClientSessionIdentity(c, body)
	if resolution.metadata.Status != OpenAIClientSessionIdentityResolved {
		if resolution.metadata.Status == OpenAIClientSessionIdentityMissing {
			return ClaudeCodeSessionIDFromHeader(c)
		}
		return ""
	}
	return resolution.identity.value
}

// InspectOpenAIClientSessionIdentity resolves the request identity and returns
// only its non-sensitive status, kind and source. Raw identity values are never
// exposed through this API.
func InspectOpenAIClientSessionIdentity(c *gin.Context, body []byte) OpenAIClientSessionIdentityMetadata {
	return resolveOpenAIClientSessionIdentity(c, body).metadata
}

func resolveOpenAIClientSessionIdentity(c *gin.Context, body []byte) openAIClientSessionIdentityResolution {
	if c == nil || c.Request == nil {
		return missingOpenAIClientSessionIdentity()
	}

	bodyThreadIdentity := openAIIdentityValue{status: OpenAIClientSessionIdentityMissing}
	bodySessionIdentity := openAIIdentityValue{status: OpenAIClientSessionIdentityMissing}
	// Escaped keys must use the original parser to preserve identity conflicts.
	if bytes.Contains(body, []byte("client_metadata")) || bytes.Contains(body, []byte(`\u`)) {
		root := parseRawJSONView(body)
		bodyThreadIdentity = openAIClientMetadataIdentityAcrossEnvelope(root, "thread_id")
		bodySessionIdentity = openAIClientMetadataIdentityAcrossEnvelope(root, "session_id")
	}
	bodyThread, invalidBodyThread := bodyThreadIdentity.value, bodyThreadIdentity.status == OpenAIClientSessionIdentityInvalid
	bodySession, invalidBodySession := bodySessionIdentity.value, bodySessionIdentity.status == OpenAIClientSessionIdentityInvalid

	headerThread := openAIIdentityHeader(c, openAIThreadIdentityHeaders)
	if headerThread.status == OpenAIClientSessionIdentityInvalid || invalidBodyThread {
		return rejectedOpenAIClientSessionIdentity(OpenAIClientSessionIdentityInvalid, openAIClientSessionKindThread, identityFailureSource(headerThread, invalidBodyThread))
	}
	if bodyThreadIdentity.status == OpenAIClientSessionIdentityConflict {
		return rejectedOpenAIClientSessionIdentity(OpenAIClientSessionIdentityConflict, openAIClientSessionKindThread, OpenAIClientSessionIdentitySourceBody)
	}
	if headerThread.status == OpenAIClientSessionIdentityConflict || openAIIdentityValuesConflict(headerThread.value, bodyThread) {
		return rejectedOpenAIClientSessionIdentity(OpenAIClientSessionIdentityConflict, openAIClientSessionKindThread, identityConflictSource(headerThread, bodyThread))
	}
	if headerThread.value != "" {
		source := OpenAIClientSessionIdentitySourceHeader
		if bodyThread != "" {
			source = OpenAIClientSessionIdentitySourceHeaderBody
		}
		return resolvedOpenAIClientSessionIdentity(openAIClientSessionKindThread, headerThread.value, source)
	}
	if bodyThread != "" {
		return resolvedOpenAIClientSessionIdentity(openAIClientSessionKindThread, bodyThread, OpenAIClientSessionIdentitySourceBody)
	}

	headerSession := openAIIdentityHeader(c, openAISessionIdentityHeaders)
	if headerSession.status == OpenAIClientSessionIdentityInvalid || invalidBodySession {
		return rejectedOpenAIClientSessionIdentity(OpenAIClientSessionIdentityInvalid, openAIClientSessionKindSession, identityFailureSource(headerSession, invalidBodySession))
	}
	if bodySessionIdentity.status == OpenAIClientSessionIdentityConflict {
		return rejectedOpenAIClientSessionIdentity(OpenAIClientSessionIdentityConflict, openAIClientSessionKindSession, OpenAIClientSessionIdentitySourceBody)
	}
	if headerSession.status == OpenAIClientSessionIdentityConflict || openAIIdentityValuesConflict(headerSession.value, bodySession) {
		return rejectedOpenAIClientSessionIdentity(OpenAIClientSessionIdentityConflict, openAIClientSessionKindSession, identityConflictSource(headerSession, bodySession))
	}
	if headerSession.value != "" {
		source := OpenAIClientSessionIdentitySourceHeader
		if bodySession != "" {
			source = OpenAIClientSessionIdentitySourceHeaderBody
		}
		return resolvedOpenAIClientSessionIdentity(openAIClientSessionKindSession, headerSession.value, source)
	}
	if bodySession != "" {
		return resolvedOpenAIClientSessionIdentity(openAIClientSessionKindSession, bodySession, OpenAIClientSessionIdentitySourceBody)
	}
	return missingOpenAIClientSessionIdentity()
}

func missingOpenAIClientSessionIdentity() openAIClientSessionIdentityResolution {
	return openAIClientSessionIdentityResolution{metadata: OpenAIClientSessionIdentityMetadata{
		Status: OpenAIClientSessionIdentityMissing,
		Source: OpenAIClientSessionIdentitySourceNone,
	}}
}

func rejectedOpenAIClientSessionIdentity(status OpenAIClientSessionIdentityStatus, kind, source string) openAIClientSessionIdentityResolution {
	return openAIClientSessionIdentityResolution{metadata: OpenAIClientSessionIdentityMetadata{
		Status: status,
		Kind:   kind,
		Source: source,
	}}
}

func resolvedOpenAIClientSessionIdentity(kind, value, source string) openAIClientSessionIdentityResolution {
	return openAIClientSessionIdentityResolution{
		metadata: OpenAIClientSessionIdentityMetadata{
			Status: OpenAIClientSessionIdentityResolved,
			Kind:   kind,
			Source: source,
		},
		identity: openAIClientSessionIdentity{kind: kind, value: value},
	}
}

func identityFailureSource(header openAIIdentityValue, invalidBody bool) string {
	if header.status == OpenAIClientSessionIdentityInvalid && invalidBody {
		return OpenAIClientSessionIdentitySourceHeaderBody
	}
	if header.status == OpenAIClientSessionIdentityInvalid {
		return OpenAIClientSessionIdentitySourceHeader
	}
	return OpenAIClientSessionIdentitySourceBody
}

func identityConflictSource(header openAIIdentityValue, body string) string {
	if header.status == OpenAIClientSessionIdentityConflict {
		return OpenAIClientSessionIdentitySourceHeader
	}
	if header.value != "" && body != "" {
		return OpenAIClientSessionIdentitySourceHeaderBody
	}
	return OpenAIClientSessionIdentitySourceNone
}

func openAIIdentityHeader(c *gin.Context, headers []string) openAIIdentityValue {
	var resolved string
	for _, header := range headers {
		raw := c.GetHeader(header)
		if strings.TrimSpace(raw) == "" {
			continue
		}
		value := sanitizeSessionID(raw)
		if value == "" {
			return openAIIdentityValue{status: OpenAIClientSessionIdentityInvalid}
		}
		if resolved != "" && resolved != value {
			return openAIIdentityValue{status: OpenAIClientSessionIdentityConflict}
		}
		resolved = value
	}
	if resolved == "" {
		return openAIIdentityValue{status: OpenAIClientSessionIdentityMissing}
	}
	return openAIIdentityValue{value: resolved, status: OpenAIClientSessionIdentityResolved}
}

// Identity validation must inspect both accepted locations. The generic payload
// view intentionally prefers an envelope for routing, but that precedence must
// not hide a conflicting or invalid identity in the other location. Merge each
// kind separately so the existing thread-before-session policy stays intact.
func openAIClientMetadataIdentityAcrossEnvelope(root gjson.Result, field string) openAIIdentityValue {
	views := []gjson.Result{root}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(root.Get("type").String())), "response.") {
		if response := root.Get("response"); response.IsObject() {
			views = append(views, response)
		}
	}
	resolved := openAIIdentityValue{status: OpenAIClientSessionIdentityMissing}
	for _, view := range views {
		if metadata := view.Get("client_metadata"); metadata.Exists() && !metadata.IsObject() {
			return openAIIdentityValue{status: OpenAIClientSessionIdentityInvalid}
		}
		value, invalid := openAIClientMetadataIdentity(view, "client_metadata."+field)
		if invalid {
			return openAIIdentityValue{status: OpenAIClientSessionIdentityInvalid}
		}
		if value == "" {
			continue
		}
		if resolved.value != "" && resolved.value != value {
			resolved.status = OpenAIClientSessionIdentityConflict
		} else {
			resolved.value = value
			resolved.status = OpenAIClientSessionIdentityResolved
		}
	}
	return resolved
}

func openAIClientMetadataIdentity(view gjson.Result, path string) (string, bool) {
	result := view.Get(path)
	if !result.Exists() {
		return "", false
	}
	if result.Type != gjson.String {
		return "", true
	}
	if strings.TrimSpace(result.String()) == "" {
		return "", false
	}
	value := sanitizeSessionID(result.String())
	return value, value == ""
}

func openAIIdentityValuesConflict(headerValue, bodyValue string) bool {
	return headerValue != "" && bodyValue != "" && headerValue != bodyValue
}

// sanitizeSessionID normalizes a raw client-supplied session identifier for safe
// persistence: it trims surrounding whitespace, rejects the value outright if it
// contains any control character (CR/LF/tab/NUL/…) so a log- or header-injection style
// payload cannot slip into stored correlation data, and rejects values longer than
// the DB column bound. Absent or invalid input yields "".
func sanitizeSessionID(raw string) string {
	if !utf8.ValidString(raw) {
		return ""
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	count := 0
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			// An explicit correlation id never legitimately contains control
			// characters; drop the whole value rather than persist a mangled or
			// partially-injected identifier.
			return ""
		}
		count++
		if count > maxPersistedSessionIDLength {
			return ""
		}
	}
	return trimmed
}
