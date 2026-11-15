package qqbot

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSpamReportQQResponseRequiresDocumentedSuccess(t *testing.T) {
	for _, raw := range []string{"", "  \r\n", "{}", " { \n } "} {
		require.NoError(t, validateSpamReportQQResponse([]byte(raw)))
	}
	for _, raw := range []string{`{"code":40062003}`, `{"code":0}`, `{"error":"denied"}`,
		`{"members":[{"success":false}]}`, "null", "[]", "true", "{} {}", "<html>error</html>", strings.Repeat(" ", 1025)} {
		require.Error(t, validateSpamReportQQResponse([]byte(raw)))
	}
}
