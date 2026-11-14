package qqbot

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"time"
)

// The pinned source used its default client to call httptest listeners. This
// explicit fixture keeps business/protocol assertions unchanged while isolating
// every test request from real QQ, model, email and payment destinations.
// Tests of the production network policy deliberately call New directly.
func newMatureFixtureService(db *sql.DB, encryptor Encryptor, ready bool, invalidate func(context.Context, int64)) *Service {
	s := New(db, encryptor, ready, invalidate)
	s.http = &http.Client{Timeout: 210 * time.Second, CheckRedirect: noRedirect, Transport: matureFixtureTransport{base: &http.Transport{Proxy: nil}}}
	return s
}

type matureFixtureTransport struct{ base *http.Transport }

func (t matureFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ip := net.ParseIP(r.URL.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("test fixture attempted a non-loopback request")
	}
	return t.base.RoundTrip(r)
}
func (t matureFixtureTransport) CloseIdleConnections() { t.base.CloseIdleConnections() }
