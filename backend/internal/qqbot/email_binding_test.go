package qqbot

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQQBotBindingEmailInputAndLink(t *testing.T) {
	for _, address := range []string{"Alice@example.com", "test+qq@example.com"} {
		require.True(t, validBindingEmail(address))
	}
	for _, address := range []string{"Alice <a@example.com>", "a@example.com\r\nBcc: other@example.com", "a@example.com b@example.com", "not-an-email"} {
		require.False(t, validBindingEmail(address))
	}
	token := strings.Repeat("a", 48)
	link, err := emailBindingURL("https://example.com/", token)
	require.NoError(t, err)
	u, err := url.Parse(link)
	require.NoError(t, err)
	require.Equal(t, "/qqbot/bind", u.Path)
	require.Empty(t, u.RawQuery)
	require.Equal(t, "token="+token, u.Fragment)
	for _, base := range []string{"", "javascript:alert(1)", "https://evil:password@example.com", "https://example.com?redirect=evil"} {
		_, err = emailBindingURL(base, token)
		require.Error(t, err)
	}
	e := Event{GroupID: "<img src=x>"}
	e.Author.Username = "<script>alert(1)</script>"
	body := bindingEmailBody(link, e)
	require.NotContains(t, body, "<script>")
	require.NotContains(t, body, "<img")
	require.Contains(t, body, "&lt;script&gt;")
}
