package qqbot

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatureQQDefaultTransportRejectsPrivateDestination(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"synthetic":true}`))
	}))
	defer upstream.Close()
	s := New(nil, nil, false, nil)
	_, _, err := s.request(context.Background(), "POST", upstream.URL+"/v1/responses", "synthetic-key", map[string]string{"input": "synthetic"}, 1024)
	require.Error(t, err, "untrusted private model destinations must be rejected before credentials leave the process")
	require.Zero(t, calls.Load(), "default transport must not reach private destinations")
}

func TestMatureQQRejectsTranslationAndSpecialPurposeIPs(t *testing.T) {
	for _, raw := range []string{"64:ff9b::7f00:1", "2002:7f00:1::", "2001:db8::1", "198.18.0.1", "192.0.2.1", "240.0.0.1"} {
		require.False(t, publicIP(net.ParseIP(raw)), "special-purpose address must not pass the public destination policy: %s", raw)
	}
}
