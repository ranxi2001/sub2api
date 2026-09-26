package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

// OpenAICodexStateVerdict 是门票（state）探针对账号智力档位的结论。
type OpenAICodexStateVerdict string

const (
	// OpenAICodexStateHealthy：带原门票续接时上游没回新门票，账号仍是满血（astra）。
	OpenAICodexStateHealthy OpenAICodexStateVerdict = "healthy"
	// OpenAICodexStateDegraded：带原门票续接时上游回了新门票，账号大概率已被降到 luna 档。
	OpenAICodexStateDegraded OpenAICodexStateVerdict = "degraded"
	// OpenAICodexStateInconclusive：两发没有都干净跑通，本次无法判断。
	OpenAICodexStateInconclusive OpenAICodexStateVerdict = "inconclusive"
)

// 探针没跑通时的原因分类（OpenAICodexStateProbeResult.Failure）。
const (
	OpenAICodexStateFailureUnsupported      = "unsupported"
	OpenAICodexStateFailureAccountError     = "account_error"
	OpenAICodexStateFailureRateLimited      = "rate_limited"
	OpenAICodexStateFailureModelUnsupported = "model_unsupported"
	OpenAICodexStateFailureUpstreamError    = "upstream_error"
	OpenAICodexStateFailureNetworkError     = "network_error"
	OpenAICodexStateFailureStreamError      = "stream_error"
	OpenAICodexStateFailureNoTicket         = "no_ticket"
	OpenAICodexStateFailureCancelled        = "cancelled"
)

const (
	OpenAICodexStateProbeDefaultModel = "gpt-6-astra"

	openAICodexStateProbeShotTimeout   = 45 * time.Second
	openAICodexStateProbeMaxBody       = 1 << 20
	openAICodexStateProbeMaxDetailByte = 300
)

// OpenAICodexStateProbeLimitation 是探针判据的适用范围说明，展示结果时应一并给出。
const OpenAICodexStateProbeLimitation = "判据只看门票头（x-codex-turn-state）：先裸发打一张票，再带这张票续接一发，上游回新门票即视作降智，不回视作满血。只发两句极短的请求、不跑整套题目；单次结果受当次上游波动影响，需结合多次趋势看。响应里回报的模型名（response.model）不作判据——降级到 luna 时它仍会回 astra。"

var ErrOpenAICodexStateProbeBusy = infraerrors.Conflict("STATE_PROBE_BUSY", "该账号已有一次探针在进行中，请稍后再试")

// OpenAICodexStateProbeResult 是一次门票探针的完整结果，可直接作为接口 JSON 返回。
type OpenAICodexStateProbeResult struct {
	AccountID int64                   `json:"account_id"`
	Model     string                  `json:"model"`
	Verdict   OpenAICodexStateVerdict `json:"verdict"`
	Reason    string                  `json:"reason"`
	Failure   string                  `json:"failure,omitempty"`
	// Detail 是脱敏截断后的上游报错原文，便于看出「模型不支持」之类的具体原因。
	Detail string `json:"detail,omitempty"`

	MintStatus     int  `json:"mint_status"`
	ContinueStatus int  `json:"continue_status"`
	Minted         bool `json:"minted"`
	NewTicket      bool `json:"new_ticket"`
	// TicketLength 是第一发打到的门票长度；判据只在 780 长度的完整请求上验证过。
	TicketLength         int    `json:"ticket_length"`
	ContinueTicketLength int    `json:"continue_ticket_length"`
	ReportedModel        string `json:"reported_model,omitempty"`

	LatencyMs  int64     `json:"latency_ms"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

type openAICodexStateShot struct {
	status  int
	state   string
	cookies []string
	model   string
	// streamErr 非空表示 200 但回复流没有完整跑完（上游过载、额度用尽等流内失败）。
	streamErr error
	detail    string
}

// ProbeOpenAICodexState 用两发极小请求判断账号是否降智：
//  1. 裸发（不带门票、不带 Cookie）打一张新票，取门票头 S1 和路由 Cookie（__cflb/__oailb）；
//  2. 带上 S1 和这两枚 Cookie 续接一发：响应头回了与 S1 不同的新门票 → 降智；没回或回原票 → 满血。
//
// 只读：不写门票库、不改账号 Extra、不影响调度；走账号自己的代理，不占用打票节点池。
// 两发必须都是 HTTP 200 且回复流完整结束才下结论，否则一律判「无法判断」，避免误报降智。
// 返回值永不为 nil。model 为空时用 gpt-6-astra，并按账号模型映射转成上游模型名。
func (s *OpenAIGatewayService) ProbeOpenAICodexState(ctx context.Context, account *Account, model string) *OpenAICodexStateProbeResult {
	started := time.Now()
	result := &OpenAICodexStateProbeResult{Verdict: OpenAICodexStateInconclusive, StartedAt: started}
	defer func() {
		result.FinishedAt = time.Now()
		result.LatencyMs = result.FinishedAt.Sub(started).Milliseconds()
		if result.Reason == "" {
			result.Reason = openAICodexStateVerdictReason(result.Verdict)
		}
	}()
	if account != nil {
		result.AccountID = account.ID
	}
	requested := strings.TrimSpace(model)
	if requested == "" {
		requested = OpenAICodexStateProbeDefaultModel
	}
	upstreamModel := requested
	if account != nil {
		upstreamModel = normalizeOpenAIModelForUpstream(account, account.GetMappedModel(requested))
	}
	result.Model = upstreamModel

	if reason := openAICodexStateProbeUnsupportedReason(account, requested); reason != "" {
		result.fail(OpenAICodexStateFailureUnsupported, reason, "")
		return result
	}
	if s == nil || s.httpUpstream == nil {
		result.fail(OpenAICodexStateFailureUnsupported, "OpenAI 网关不可用，无法发起探针", "")
		return result
	}

	token, authMode, err := s.GetAccessToken(ctx, account)
	if err != nil {
		result.fail(OpenAICodexStateFailureAccountError, "账号认证失败，无法取得访问令牌", SanitizeUpstreamErrorMessage(truncateString(err.Error(), openAICodexStateProbeMaxDetailByte)))
		return result
	}
	if strings.TrimSpace(token) == "" {
		if authMode == OpenAIAuthModeAgentIdentity {
			result.fail(OpenAICodexStateFailureUnsupported, "Agent Identity 账号不使用门票，探针不适用", "")
			return result
		}
		result.fail(OpenAICodexStateFailureAccountError, "账号认证失败，访问令牌为空", "")
		return result
	}
	proxy := openAIAccountProxyURL(account)

	mint, err := s.fireOpenAICodexStateShot(ctx, account, token, upstreamModel, proxy, "", "")
	result.MintStatus = mint.status
	result.TicketLength = len(mint.state)
	if !result.shotUsable(ctx, "打票", mint, err) {
		return result
	}
	if mint.state == "" {
		result.fail(OpenAICodexStateFailureNoTicket, "打票成功但未返回门票头（x-codex-turn-state），无法进行续接判据", "")
		return result
	}
	result.Minted = true

	cont, err := s.fireOpenAICodexStateShot(ctx, account, token, upstreamModel, proxy, mint.state, strings.Join(mint.cookies, "; "))
	result.ContinueStatus = cont.status
	result.ContinueTicketLength = len(cont.state)
	if !result.shotUsable(ctx, "门票续接", cont, err) {
		return result
	}

	result.NewTicket = cont.state != "" && cont.state != mint.state
	result.ReportedModel = cont.model
	if result.ReportedModel == "" {
		result.ReportedModel = mint.model
	}
	if result.NewTicket {
		result.Verdict = OpenAICodexStateDegraded
	} else {
		result.Verdict = OpenAICodexStateHealthy
	}
	return result
}

func (r *OpenAICodexStateProbeResult) fail(kind, reason, detail string) {
	r.Verdict = OpenAICodexStateInconclusive
	r.Failure = kind
	r.Reason = reason
	r.Detail = detail
}

// shotUsable 判断一发是否干净跑通；没跑通时写好失败分类并返回 false。
func (r *OpenAICodexStateProbeResult) shotUsable(ctx context.Context, step string, shot openAICodexStateShot, err error) bool {
	if err != nil {
		if ctx.Err() != nil {
			r.fail(OpenAICodexStateFailureCancelled, step+"请求被取消或超时", "")
			return false
		}
		if errors.Is(err, context.DeadlineExceeded) {
			r.fail(OpenAICodexStateFailureNetworkError, fmt.Sprintf("%s请求超时（%d 秒内没有完成）", step, int(openAICodexStateProbeShotTimeout/time.Second)), "")
			return false
		}
		r.fail(OpenAICodexStateFailureNetworkError, step+"请求失败（网络或代理异常）", SanitizeUpstreamErrorMessage(truncateString(err.Error(), openAICodexStateProbeMaxDetailByte)))
		return false
	}
	switch {
	case shot.status == http.StatusOK:
		if shot.streamErr != nil {
			r.fail(OpenAICodexStateFailureStreamError, step+"返回 200 但回复流没有正常结束（上游过载或额度用尽等），本次无法判断", shot.detail)
			return false
		}
		return true
	case shot.status == http.StatusUnauthorized || shot.status == http.StatusForbidden:
		r.fail(OpenAICodexStateFailureAccountError, fmt.Sprintf("%s被上游拒绝（HTTP %d），账号令牌可能失效或被封", step, shot.status), shot.detail)
	case shot.status == http.StatusTooManyRequests:
		r.fail(OpenAICodexStateFailureRateLimited, step+"被上游限流（HTTP 429），请稍后再试", shot.detail)
	case shot.status == http.StatusBadRequest && strings.Contains(strings.ToLower(shot.detail), "not supported"):
		r.fail(OpenAICodexStateFailureModelUnsupported, "该账号的套餐不支持这个模型（HTTP 400），请换一个模型再测", shot.detail)
	default:
		r.fail(OpenAICodexStateFailureUpstreamError, fmt.Sprintf("%s返回异常状态（HTTP %d）", step, shot.status), shot.detail)
	}
	return false
}

func openAICodexStateVerdictReason(verdict OpenAICodexStateVerdict) string {
	switch verdict {
	case OpenAICodexStateDegraded:
		return "带原门票续接时上游回了新的门票（x-codex-turn-state），符合降智特征：账号很可能已被降到 gpt-5.6-luna 档，建议尽快换号。"
	case OpenAICodexStateHealthy:
		return "带原门票续接时上游没有回新门票，门票延续正常：账号仍处于满血（gpt-6-astra）状态。"
	default:
		return "未取得有效的门票续接探测结果，无法判断账号是否降智。"
	}
}

// openAICodexStateProbeUnsupportedReason 返回空串表示该账号可以跑探针。
// 凭证影子账号可以跑：令牌和 chatgpt-account-id 都会解析到母账号。
func openAICodexStateProbeUnsupportedReason(account *Account, requestedModel string) string {
	switch {
	case account == nil:
		return "账号不存在"
	case !account.IsOpenAIOAuthLike():
		return "探针只适用于 OpenAI ChatGPT 订阅（OAuth）账号"
	case account.IsSyntheticUITest():
		return "测试数据账号不向上游发真实请求，探针不适用"
	case account.IsOpenAIAgentIdentity():
		return "Agent Identity 账号不使用门票，探针不适用"
	case account.IsExcelBPSEnabledForModel(requestedModel):
		return "该账号的这个模型走 Excel/BPS 通道，不经过门票，探针不适用"
	}
	return ""
}

// fireOpenAICodexStateShot 发一发与真实 Codex 客户端同形的极小请求并读完回复流；
// turnState / cookie 为空时就是裸发打票。
//
// 这里刻意不带 x-openai-internal-codex-responses-lite 头、也不复用打票（harvest）的请求体：
// 「续接回新票 = 降智」这条判据只在这种完整请求（门票长度 780）上实测验证过，
// lite 形态（292/332 长度的票）上没有验证。每发用新的 session_id，也与验证时一致。
func (s *OpenAIGatewayService) fireOpenAICodexStateShot(ctx context.Context, account *Account, token, model, proxy, turnState, cookie string) (openAICodexStateShot, error) {
	var out openAICodexStateShot
	shotCtx, cancel := context.WithTimeout(ctx, openAICodexStateProbeShotTimeout)
	defer cancel()

	body := []byte(`{"model":` + jsonString(model) + `,"instructions":"Reply with OK.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Reply with OK."}]}],"stream":true,"store":false,"parallel_tool_calls":true,"include":["reasoning.encrypted_content"]}`)
	req, err := http.NewRequestWithContext(shotCtx, http.MethodPost, chatgptCodexURL, bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAIHarvest))
	req.Close = true
	req.Host = "chatgpt.com"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("session_id", uuid.NewString())
	if err := resolveAndSetOpenAIChatGPTAccountHeaders(shotCtx, s.accountRepo, req.Header, account); err != nil {
		return out, err
	}
	applyOpenAICodexTicketHarvestIdentity(req.Header, model)
	req.Header.Del(responsesLiteHeaderKey)
	if turnState = strings.TrimSpace(turnState); turnState != "" {
		req.Header.Set(openAICodexTurnStateHeader, turnState)
	}
	if cookie = strings.TrimSpace(cookie); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return out, err
	}
	if resp == nil {
		return out, errors.New("nil upstream response")
	}
	out.status = resp.StatusCode
	out.state = strings.TrimSpace(extractOpenAICodexTurnState(resp.Header))
	out.cookies = openAICodexStateRouteCookies(resp)
	if resp.Body == nil {
		out.streamErr = errors.New("probe response body missing")
		return out, nil
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, openAICodexStateProbeMaxBody+1))
	if err != nil {
		if shotCtx.Err() != nil {
			return out, shotCtx.Err()
		}
		out.streamErr = errors.New("probe response incomplete")
		return out, nil
	}
	if len(data) > openAICodexStateProbeMaxBody {
		out.streamErr = errors.New("probe response too large")
		return out, nil
	}
	if out.status != http.StatusOK {
		out.detail = openAICodexStateDetail(data)
		return out, nil
	}
	out.model = openAICodexStateStreamModel(data)
	if out.streamErr = validateCodexProbeResponse(data); out.streamErr != nil {
		out.detail = openAICodexStateDetail(openAICodexStateStreamErrorPayload(data))
	}
	return out, nil
}

// openAICodexStateRouteCookies 只留两枚负载均衡路由 Cookie；门票要搭配它们续接才有意义。
func openAICodexStateRouteCookies(resp *http.Response) []string {
	var out []string
	seen := map[string]bool{}
	for _, cookie := range resp.Cookies() {
		if cookie.Name != "__cflb" && cookie.Name != "__oailb" {
			continue
		}
		if seen[cookie.Name] || cookie.Value == "" || cookie.MaxAge < 0 {
			continue
		}
		seen[cookie.Name] = true
		out = append(out, cookie.Name+"="+cookie.Value)
	}
	return out
}

func openAICodexStateDetail(body []byte) string {
	msg := strings.TrimSpace(extractUpstreamErrorMessage(body))
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	return SanitizeUpstreamErrorMessage(truncateString(msg, openAICodexStateProbeMaxDetailByte))
}

// openAICodexStateStreamEvents 按 SSE 事件切出每个 data 负载（跳过 [DONE]）。
func openAICodexStateStreamEvents(data []byte) [][]byte {
	var events [][]byte
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), openAICodexStateProbeMaxBody)
	var lines []string
	flush := func() {
		if len(lines) == 0 {
			return
		}
		payload := strings.TrimSpace(strings.Join(lines, "\n"))
		lines = nil
		if payload != "" && payload != "[DONE]" {
			events = append(events, []byte(payload))
		}
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	flush()
	return events
}

// openAICodexStateStreamModel 取上游声明的响应模型：response.created 优先，其次 response.completed。
// 只用于展示，不作判据。
func openAICodexStateStreamModel(data []byte) string {
	completed := ""
	for _, payload := range openAICodexStateStreamEvents(data) {
		var event struct {
			Type     string `json:"type"`
			Response struct {
				Model string `json:"model"`
			} `json:"response"`
		}
		if json.Unmarshal(payload, &event) != nil {
			continue
		}
		model := strings.TrimSpace(event.Response.Model)
		if model == "" {
			continue
		}
		switch event.Type {
		case "response.created":
			return model
		case "response.completed":
			completed = model
		}
	}
	return completed
}

// openAICodexStateStreamErrorPayload 取流里第一个失败事件，供提取上游报错原文。
func openAICodexStateStreamErrorPayload(data []byte) []byte {
	for _, payload := range openAICodexStateStreamEvents(data) {
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(payload, &event) != nil {
			continue
		}
		switch event.Type {
		case "error", "response.failed", "response.incomplete":
			var wrapped struct {
				Response struct {
					Error json.RawMessage `json:"error"`
				} `json:"response"`
			}
			if json.Unmarshal(payload, &wrapped) == nil && len(wrapped.Response.Error) > 0 && string(wrapped.Response.Error) != "null" {
				return []byte(`{"error":` + string(wrapped.Response.Error) + `}`)
			}
			return payload
		}
	}
	return nil
}

// ProbeOpenAICodexState 按账号 ID 跑一次门票探针。同一账号同一时刻只允许一次探针，
// 避免并发的两发互相干扰门票判据。
func (s *AccountTestService) ProbeOpenAICodexState(ctx context.Context, accountID int64, model string) (*OpenAICodexStateProbeResult, error) {
	if s == nil || s.accountRepo == nil {
		return nil, errors.New("account test service unavailable")
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrAccountNotFound
	}
	release, ok := s.beginOpenAICodexStateProbe(accountID)
	if !ok {
		return nil, ErrOpenAICodexStateProbeBusy
	}
	defer release()
	return s.openaiGatewayService.ProbeOpenAICodexState(ctx, account, model), nil
}

func (s *AccountTestService) beginOpenAICodexStateProbe(accountID int64) (func(), bool) {
	if _, loaded := s.stateProbeAccounts.LoadOrStore(accountID, struct{}{}); loaded {
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(func() { s.stateProbeAccounts.Delete(accountID) }) }, true
}
