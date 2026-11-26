package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIssue1077RegionalFinalizerOnlyCertifiedUnsentHandshake(t *testing.T) {
	for _, tc := range []struct {
		name                                  string
		sent, canceled, wrongAttempt, generic bool
	}{
		{name: "canceled_unsent", canceled: true},
		{name: "cancel_unknown"},
		{name: "sent", sent: true, canceled: true},
		{name: "another_attempt", canceled: true, wrongAttempt: true},
		{name: "generic_error", canceled: true, generic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &regionalMeterAttempt{}
			a.sent.Store(tc.sent)
			a.canceledBeforeStart.Store(tc.canceled)
			ctx := context.WithValue(context.Background(), regionalMeterKey{}, a)
			failure := &UpstreamFailoverError{StatusCode: 401}
			certified := &openAIWSHandshakeAccountFailover{cause: failure, meter: a}
			if tc.wrongAttempt {
				certified.meter = &regionalMeterAttempt{}
			}
			var forwardErr error = certified
			if tc.generic {
				forwardErr = failure
			}
			err := regionalMeterFinishForward(ctx, nil, forwardErr, func(*OpenAIForwardResult, error) error { return nil })
			var replay *UpstreamFailoverError
			require.Equal(t, tc.canceled && !tc.sent && !tc.wrongAttempt && !tc.generic, errors.As(err, &replay))
		})
	}
}
