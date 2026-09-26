package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type stateProbeReply struct {
	status  int
	state   string
	cookies []string
	body    string
	err     error
}

type stateProbeCall struct {
	header http.Header
	body   string
	proxy  string
	close  bool
}

type stateProbeUpstream struct {
	mu      sync.Mutex
	replies []stateProbeReply
	calls   []stateProbeCall
}

func (u *stateProbeUpstream) Do(req *http.Request, proxyURL string, _ int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	raw, _ := io.ReadAll(req.Body)
	u.calls = append(u.calls, stateProbeCall{header: req.Header.Clone(), body: string(raw), proxy: proxyURL, close: req.Close})
	if len(u.replies) == 0 {
		return nil, errors.New("unexpected upstream call")
	}
	reply := u.replies[0]
	u.replies = u.replies[1:]
	if reply.err != nil {
		return nil, reply.err
	}
	h := http.Header{}
	if reply.state != "" {
		h.Set(openAICodexTurnStateHeader, reply.state)
	}
	for _, c := range reply.cookies {
		h.Add("Set-Cookie", c)
	}
	return &http.Response{StatusCode: reply.status, Header: h, Body: io.NopCloser(strings.NewReader(reply.body))}, nil
}

func (u *stateProbeUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

type stateProbeAccountRepo struct {
	AccountRepository
	account *Account
}

func (r *stateProbeAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	if r.account == nil {
		return nil, ErrAccountNotFound
	}
	return r.account, nil
}

const stateProbeCompletedStream = "event: response.created\n" +
	`data: {"type":"response.created","response":{"model":"gpt-6-astra","status":"in_progress"}}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"model":"gpt-6-astra","status":"completed"}}` + "\n\n"

const stateProbeFailedStream = "event: response.created\n" +
	`data: {"type":"response.created","response":{"model":"gpt-6-astra","status":"in_progress"}}` + "\n\n" +
	"event: response.failed\n" +
	`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded."}}}` + "\n\n"

func stateProbeAccount() *Account {
	return &Account{
		ID:          7,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "tok-7", "chatgpt_account_id": "acct-7"},
	}
}

func stateProbeMint(state string) stateProbeReply {
	return stateProbeReply{
		status: http.StatusOK,
		state:  state,
		cookies: []string{
			"__cflb=cf-route; Path=/; HttpOnly",
			"__oailb=oai-route; Path=/; HttpOnly",
			"_cfuvid=visitor; Path=/",
		},
		body: stateProbeCompletedStream,
	}
}

func runStateProbe(t *testing.T, account *Account, replies ...stateProbeReply) (*OpenAICodexStateProbeResult, *stateProbeUpstream) {
	t.Helper()
	upstream := &stateProbeUpstream{replies: replies}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	result := svc.ProbeOpenAICodexState(context.Background(), account, "")
	require.NotNil(t, result)
	return result, upstream
}

func TestProbeOpenAICodexStateHealthyWhenContinuationKeepsTicket(t *testing.T) {
	minted := strings.Repeat("s", 780)
	result, upstream := runStateProbe(t, stateProbeAccount(),
		stateProbeMint(minted),
		stateProbeReply{status: http.StatusOK, body: stateProbeCompletedStream},
	)

	require.Equal(t, OpenAICodexStateHealthy, result.Verdict)
	require.Empty(t, result.Failure)
	require.True(t, result.Minted)
	require.False(t, result.NewTicket)
	require.Equal(t, 780, result.TicketLength)
	require.Equal(t, 0, result.ContinueTicketLength)
	require.Equal(t, http.StatusOK, result.MintStatus)
	require.Equal(t, http.StatusOK, result.ContinueStatus)
	require.Equal(t, "gpt-6-astra", result.ReportedModel)
	require.Equal(t, int64(7), result.AccountID)
	require.NotEmpty(t, result.Model)
	require.Contains(t, result.Reason, "满血")

	require.Len(t, upstream.calls, 2)
	mint, cont := upstream.calls[0], upstream.calls[1]
	for _, call := range upstream.calls {
		require.True(t, call.close)
		require.Empty(t, call.header.Get(responsesLiteHeaderKey))
		require.Equal(t, "Bearer tok-7", call.header.Get("Authorization"))
		require.Equal(t, "acct-7", call.header.Get("chatgpt-account-id"))
		require.Equal(t, "text/event-stream", call.header.Get("Accept"))
		require.Equal(t, "responses=experimental", call.header.Get("OpenAI-Beta"))
		require.NotEmpty(t, call.header.Get("session_id"))
		require.Contains(t, call.body, `"model":"`+result.Model+`"`)
		require.Contains(t, call.body, `"instructions":"Reply with OK."`)
		require.Contains(t, call.body, `"parallel_tool_calls":true`)
		require.Contains(t, call.body, `"include":["reasoning.encrypted_content"]`)
	}
	require.Empty(t, mint.header.Get(openAICodexTurnStateHeader))
	require.Empty(t, mint.header.Get("Cookie"))
	require.Equal(t, minted, cont.header.Get(openAICodexTurnStateHeader))
	require.Equal(t, "__cflb=cf-route; __oailb=oai-route", cont.header.Get("Cookie"))
	require.NotEqual(t, mint.header.Get("session_id"), cont.header.Get("session_id"))
}

func TestProbeOpenAICodexStateHealthyWhenContinuationEchoesTicket(t *testing.T) {
	result, _ := runStateProbe(t, stateProbeAccount(),
		stateProbeMint("ticket-1"),
		stateProbeReply{status: http.StatusOK, state: "ticket-1", body: stateProbeCompletedStream},
	)
	require.Equal(t, OpenAICodexStateHealthy, result.Verdict)
	require.False(t, result.NewTicket)
}

func TestProbeOpenAICodexStateDegradedWhenContinuationIssuesNewTicket(t *testing.T) {
	result, _ := runStateProbe(t, stateProbeAccount(),
		stateProbeMint(strings.Repeat("a", 780)),
		stateProbeReply{status: http.StatusOK, state: strings.Repeat("b", 780), body: stateProbeCompletedStream},
	)
	require.Equal(t, OpenAICodexStateDegraded, result.Verdict)
	require.True(t, result.NewTicket)
	require.Equal(t, 780, result.ContinueTicketLength)
	require.Contains(t, result.Reason, "降智")
}

func TestProbeOpenAICodexStateInconclusiveWithoutMintedTicket(t *testing.T) {
	result, upstream := runStateProbe(t, stateProbeAccount(),
		stateProbeReply{status: http.StatusOK, body: stateProbeCompletedStream},
	)
	require.Equal(t, OpenAICodexStateInconclusive, result.Verdict)
	require.Equal(t, OpenAICodexStateFailureNoTicket, result.Failure)
	require.False(t, result.Minted)
	require.Len(t, upstream.calls, 1)
}

func TestProbeOpenAICodexStateReportsUnsupportedModel(t *testing.T) {
	result, upstream := runStateProbe(t, stateProbeAccount(),
		stateProbeReply{status: http.StatusBadRequest, body: `{"detail":"x","error":{"message":"The 'gpt-6-astra' model is not supported when using Codex with a ChatGPT account."}}`},
	)
	require.Equal(t, OpenAICodexStateInconclusive, result.Verdict)
	require.Equal(t, OpenAICodexStateFailureModelUnsupported, result.Failure)
	require.Equal(t, http.StatusBadRequest, result.MintStatus)
	require.Contains(t, result.Detail, "not supported")
	require.Len(t, upstream.calls, 1)
}

func TestProbeOpenAICodexStateRateLimitedContinuationIsInconclusive(t *testing.T) {
	result, _ := runStateProbe(t, stateProbeAccount(),
		stateProbeMint("ticket-1"),
		stateProbeReply{status: http.StatusTooManyRequests, state: "ticket-2", body: `{"error":{"message":"rate limited"}}`},
	)
	require.Equal(t, OpenAICodexStateInconclusive, result.Verdict)
	require.Equal(t, OpenAICodexStateFailureRateLimited, result.Failure)
	require.True(t, result.Minted)
	require.False(t, result.NewTicket)
	require.Equal(t, http.StatusTooManyRequests, result.ContinueStatus)
}

func TestProbeOpenAICodexStateFailedStreamIsNotDegraded(t *testing.T) {
	result, _ := runStateProbe(t, stateProbeAccount(),
		stateProbeMint("ticket-1"),
		stateProbeReply{status: http.StatusOK, state: "ticket-2", body: stateProbeFailedStream},
	)
	require.Equal(t, OpenAICodexStateInconclusive, result.Verdict)
	require.Equal(t, OpenAICodexStateFailureStreamError, result.Failure)
	require.False(t, result.NewTicket)
	require.Contains(t, result.Detail, "overloaded")
}

func TestProbeOpenAICodexStateNetworkError(t *testing.T) {
	result, _ := runStateProbe(t, stateProbeAccount(),
		stateProbeReply{err: errors.New("proxy connect refused")},
	)
	require.Equal(t, OpenAICodexStateInconclusive, result.Verdict)
	require.Equal(t, OpenAICodexStateFailureNetworkError, result.Failure)
	require.Contains(t, result.Detail, "proxy connect refused")
}

func TestProbeOpenAICodexStateRejectsUnsupportedAccounts(t *testing.T) {
	apiKey := stateProbeAccount()
	apiKey.Type = AccountTypeAPIKey
	synthetic := stateProbeAccount()
	synthetic.Extra = map[string]any{"synthetic_ui_test": true}
	agent := stateProbeAccount()
	agent.Credentials[openAIAuthModeCredentialKey] = OpenAIAuthModeAgentIdentity
	bps := stateProbeAccount()
	bps.Extra = map[string]any{"openai_excel_bps": true}

	for name, account := range map[string]*Account{"nil": nil, "apikey": apiKey, "synthetic": synthetic, "agent": agent, "bps": bps} {
		t.Run(name, func(t *testing.T) {
			result, upstream := runStateProbe(t, account)
			require.Equal(t, OpenAICodexStateInconclusive, result.Verdict)
			require.Equal(t, OpenAICodexStateFailureUnsupported, result.Failure)
			require.Empty(t, upstream.calls)
		})
	}
}

func TestAccountTestServiceStateProbeIsExclusivePerAccount(t *testing.T) {
	account := stateProbeAccount()
	upstream := &stateProbeUpstream{replies: []stateProbeReply{
		stateProbeMint("ticket-1"),
		{status: http.StatusOK, body: stateProbeCompletedStream},
	}}
	svc := &AccountTestService{
		accountRepo:          &stateProbeAccountRepo{account: account},
		openaiGatewayService: &OpenAIGatewayService{httpUpstream: upstream},
	}

	release, ok := svc.beginOpenAICodexStateProbe(account.ID)
	require.True(t, ok)
	_, err := svc.ProbeOpenAICodexState(context.Background(), account.ID, "")
	require.ErrorIs(t, err, ErrOpenAICodexStateProbeBusy)
	require.Empty(t, upstream.calls)

	release()
	result, err := svc.ProbeOpenAICodexState(context.Background(), account.ID, "")
	require.NoError(t, err)
	require.Equal(t, OpenAICodexStateHealthy, result.Verdict)

	_, again := svc.beginOpenAICodexStateProbe(account.ID)
	require.True(t, again)
}

func TestAccountTestServiceStateProbeMissingAccount(t *testing.T) {
	svc := &AccountTestService{accountRepo: &stateProbeAccountRepo{}}
	_, err := svc.ProbeOpenAICodexState(context.Background(), 99, "")
	require.ErrorIs(t, err, ErrAccountNotFound)
}
