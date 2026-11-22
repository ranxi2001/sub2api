//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestStagedBillingBatchAmbiguousCommitRequiresEveryReceipt(t *testing.T) {
	for _, state := range []string{"complete", "pending", "missing"} {
		t.Run(state, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			keys := []service.UsageBillingEventKey{{RequestID: "one", APIKeyID: 2}, {RequestID: "two", APIKeyID: 2}}
			journal := sqlmock.NewRows([]string{"request_id", "api_key_id", "schema_version", "payload", "state", "result", "request_fingerprint"})
			for _, key := range keys {
				cmd := service.UsageBillingCommand{RequestID: key.RequestID, APIKeyID: 2, UserID: 1, AccountID: 3}
				cmd.Normalize()
				payload, err := json.Marshal(service.UsageBillingEvent{Version: 1, Command: cmd, Usage: service.UsageLog{RequestID: key.RequestID, APIKeyID: 2, UserID: 1, AccountID: 3, CreatedAt: time.Now()}})
				require.NoError(t, err)
				journal.AddRow(key.RequestID, key.APIKeyID, 1, payload, "applied", `{"Applied":true,"NewBalance":99}`, cmd.RequestFingerprint)
			}
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT e.request_id,e.api_key_id,e.schema_version").WillReturnRows(journal)
			mock.ExpectCommit().WillReturnError(errors.New("commit response lost"))
			confirmed := sqlmock.NewRows([]string{"request_id", "api_key_id", "state", "result"}).AddRow("one", 2, "complete", `{"Applied":true,"NewBalance":42}`)
			if state != "missing" {
				confirmed.AddRow("two", 2, state, `{"Applied":true,"NewBalance":41}`)
			}
			mock.ExpectQuery("SELECT e.request_id,e.api_key_id,e.state,e.result").WillReturnRows(confirmed)
			out, err := (&usageBillingRepository{db: db}).applyStagedUsageBillingBatch(context.Background(), "1", keys)
			if state == "complete" {
				require.NoError(t, err)
				require.Len(t, out, 2)
				require.Equal(t, 42.0, *out[0].Result.NewBalance, "authoritative stored result replaces speculative result")
				for _, r := range out {
					require.True(t, r.Settled)
					require.False(t, r.Result.Applied)
					require.False(t, r.NeedsCache)
				}
			} else {
				require.ErrorIs(t, err, service.ErrUsageBillingOutcomeUnknown)
				require.Nil(t, out)
			}
			require.NoError(t, mock.ExpectationsWereMet(), "an ambiguous commit cannot issue monetary replay")
		})
	}
}
