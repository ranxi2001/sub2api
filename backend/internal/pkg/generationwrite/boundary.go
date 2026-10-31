// Package generationwrite carries a body-free prewrite contract to transports.
// Installing it never sends a request and never retries an uncertain write.
package generationwrite

import (
	"context"
	"net/http"
	"net/http/httptrace"
	"sync"
)

type key struct{}
type Boundary func(context.Context) error

func With(req *http.Request, before Boundary) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), key{}, before))
}

// HTTP calls the admission function synchronously after slot/dial/TLS wait,
// before net/http receives the connection for writing. Cancellation is scoped
// to this request; shared HTTP/2 connections and already-sent turns keep running.
func HTTP(req *http.Request) (*http.Request, func(), func(error) error) {
	before, _ := req.Context().Value(key{}).(Boundary)
	if before == nil {
		return req, func() {}, func(err error) error { return err }
	}
	ctx, cancel := context.WithCancelCause(req.Context())
	var mu sync.Mutex
	var denied error
	trace := &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) {
		if err := before(ctx); err != nil {
			mu.Lock()
			denied = err
			mu.Unlock()
			cancel(err)
		}
	}}
	guarded := req.WithContext(httptrace.WithClientTrace(ctx, trace))
	return guarded, func() { cancel(nil) }, func(err error) error {
		mu.Lock()
		defer mu.Unlock()
		if denied != nil {
			return denied
		}
		return err
	}
}
