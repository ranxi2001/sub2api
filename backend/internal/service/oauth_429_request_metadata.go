package service

import (
	"context"

	"github.com/tidwall/gjson"
)

type oauth429WireModelKey struct{}

// WithOAuth429WireModel binds the final model at request construction, before
// transport wrappers can replace GetBody or begin sending a streaming body.
// The public input model remains in ctxkey.Model for whitelist validation.
func WithOAuth429WireModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, oauth429WireModelKey{}, model)
}

func withOAuth429WireBody(ctx context.Context, body []byte) context.Context {
	model := gjson.GetBytes(body, "model").String()
	if model == "" {
		model = gjson.GetBytes(body, "request.model").String()
	}
	if model == "" {
		return ctx
	}
	return WithOAuth429WireModel(ctx, model)
}
