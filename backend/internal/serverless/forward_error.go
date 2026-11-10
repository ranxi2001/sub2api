package serverless

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
)

// forwardErrorKind returns fixed labels, never error messages or request URLs.
func forwardErrorKind(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if err != nil && strings.Contains(err.Error(), "timeout awaiting response headers") {
		return "response_header_timeout"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return "dns_error"
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "eof"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "connection_refused"
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return "connection_reset"
	}
	var networkError *net.OpError
	if errors.As(err, &networkError) {
		return "network_error"
	}
	return "transport_error"
}
