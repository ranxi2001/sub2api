package service

import (
	"context"
	"time"
)

// HTTPUpstreamProfile marks HTTP upstream requests that need provider-specific
// transport policy.
type HTTPUpstreamProfile string

const (
	HTTPUpstreamProfileDefault       HTTPUpstreamProfile = ""
	HTTPUpstreamProfileOpenAI        HTTPUpstreamProfile = "openai"
	HTTPUpstreamProfileOpenAIHarvest HTTPUpstreamProfile = "openai_harvest"
	HTTPUpstreamProfileGrok          HTTPUpstreamProfile = "grok"
	HTTPUpstreamProfileLongStream    HTTPUpstreamProfile = "long_stream"
	HTTPUpstreamProfileExcelBPS      HTTPUpstreamProfile = "excel_bps"
)

type httpUpstreamProfileContextKey struct{}
type openAICodexHTTPRequestContextKey struct{}
type httpUpstreamDisableRedirectsContextKey struct{}
type httpUpstreamPublicHostsOnlyContextKey struct{}
type httpUpstreamResponseHeaderTimeoutContextKey struct{}

// WithHTTPUpstreamProfile injects an upstream transport profile into ctx.
func WithHTTPUpstreamProfile(ctx context.Context, profile HTTPUpstreamProfile) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if profile == HTTPUpstreamProfileDefault {
		return ctx
	}
	return context.WithValue(ctx, httpUpstreamProfileContextKey{}, profile)
}

// HTTPUpstreamProfileFromContext resolves the upstream transport profile from ctx.
func HTTPUpstreamProfileFromContext(ctx context.Context) HTTPUpstreamProfile {
	if ctx == nil {
		return HTTPUpstreamProfileDefault
	}
	profile, ok := ctx.Value(httpUpstreamProfileContextKey{}).(HTTPUpstreamProfile)
	if !ok {
		return HTTPUpstreamProfileDefault
	}
	switch profile {
	case HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileOpenAIHarvest, HTTPUpstreamProfileGrok, HTTPUpstreamProfileLongStream, HTTPUpstreamProfileExcelBPS:
		return profile
	default:
		return HTTPUpstreamProfileDefault
	}
}

// WithOpenAICodexHTTPRequest marks requests built for the first-party Codex backend.
func WithOpenAICodexHTTPRequest(ctx context.Context) context.Context {
	return context.WithValue(ctx, openAICodexHTTPRequestContextKey{}, true)
}

func IsOpenAICodexHTTPRequest(ctx context.Context) bool {
	marked, _ := ctx.Value(openAICodexHTTPRequestContextKey{}).(bool)
	return marked
}

// Keep single-submission and send-evidence paths on their original wire body.
func UpstreamCompressionRequiresPlainBody(ctx context.Context) bool {
	profile := HTTPUpstreamProfileFromContext(ctx)
	return profile == HTTPUpstreamProfileExcelBPS || profile == HTTPUpstreamProfileOpenAIHarvest ||
		isControlledExperiment(ctx) || regionalMeterAttemptFrom(ctx) != nil || OpenAIAgentRecoveryNoReplay(ctx) ||
		IsOpenAICodexStateProbeRequest(ctx) || IsAstraSourceAcquisition(ctx)
}

// WithHTTPUpstreamResponseHeaderTimeout asks for a longer response-header wait
// than the transport profile. It never shortens or enables a timeout.
func WithHTTPUpstreamResponseHeaderTimeout(ctx context.Context, timeout time.Duration) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		return ctx
	}
	return context.WithValue(ctx, httpUpstreamResponseHeaderTimeoutContextKey{}, timeout)
}

// HTTPUpstreamResponseHeaderTimeoutFromContext returns the requested wait, or zero.
func HTTPUpstreamResponseHeaderTimeoutFromContext(ctx context.Context) time.Duration {
	if ctx == nil {
		return 0
	}
	timeout, _ := ctx.Value(httpUpstreamResponseHeaderTimeoutContextKey{}).(time.Duration)
	return timeout
}

// WithHTTPUpstreamRedirectsDisabled prevents credential-bearing probes from
// following redirects through the shared upstream client.
func WithHTTPUpstreamRedirectsDisabled(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, httpUpstreamDisableRedirectsContextKey{}, true)
}

func HTTPUpstreamRedirectsDisabled(ctx context.Context) bool {
	return ctx != nil && ctx.Value(httpUpstreamDisableRedirectsContextKey{}) == true
}

// WithHTTPUpstreamPublicHostsOnly marks a request whose destination, and every
// redirect hop after it, must resolve to a public address. The shared upstream
// client enforces it regardless of the security.url_allowlist configuration;
// use it for fetches whose URL comes from an untrusted upstream response.
func WithHTTPUpstreamPublicHostsOnly(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, httpUpstreamPublicHostsOnlyContextKey{}, true)
}

func HTTPUpstreamPublicHostsOnly(ctx context.Context) bool {
	return ctx != nil && ctx.Value(httpUpstreamPublicHostsOnlyContextKey{}) == true
}
