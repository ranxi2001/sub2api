//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// RouteIssue842OAuthSocketsToLoopbackForTest retains the production OAuth
// builder, admission and frame writer. Only its fixed external dial address is
// replaced, by the real socket dialer, in a unit-only localhost fixture.
func RouteIssue842OAuthSocketsToLoopbackForTest(s *OpenAIGatewayService, target string) error {
	u, err := url.Parse(target)
	if err != nil || u == nil || u.Hostname() != "127.0.0.1" || u.Scheme != "http" {
		return fmt.Errorf("Issue842 socket fixture requires loopback HTTP")
	}
	u.Scheme = "ws"
	u.Path = "/v1/responses"
	dialer := &issue842LoopbackDialer{target: u.String(), delegate: newConfiguredOpenAIWSClientDialer(s.cfg)}
	s.getOpenAIWSConnPool().setClientDialerForTest(dialer)
	s.openaiWSPassthroughDialerOnce.Do(func() { s.openaiWSPassthroughDialer = dialer })
	return nil
}

type issue842LoopbackDialer struct {
	target   string
	delegate openAIWSClientDialer
}

func (d *issue842LoopbackDialer) Dial(ctx context.Context, address string, headers http.Header, proxy string) (openAIWSClientConn, int, http.Header, error) {
	if address != "wss://chatgpt.com/backend-api/codex/responses" || proxy != "" {
		return nil, 0, nil, fmt.Errorf("Issue842 refuses unexpected OAuth socket destination")
	}
	return d.delegate.Dial(ctx, d.target, headers, "")
}
