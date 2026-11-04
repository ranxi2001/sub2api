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

func TestUsageBillingDeliveryCommitUnknownChecksReceiptBeforeReplay(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	cmd := service.UsageBillingCommand{RequestID: "unknown", APIKeyID: 2, UserID: 1, AccountID: 3, BalanceCost: 1}
	cmd.Normalize()
	e := service.UsageBillingEvent{Version: 1, Command: cmd, Usage: service.UsageLog{
		RequestID: "unknown", APIKeyID: 2, UserID: 1, AccountID: 3, ActualCost: 1, Model: "mock", CreatedAt: time.Now(),
	}}
	payload, err := json.Marshal(e)
	require.NoError(t, err)
	result := service.UsageBillingApplyResult{Applied: true}
	storedResult, err := json.Marshal(result)
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT schema_version,payload,state,result,request_fingerprint").WillReturnRows(
		sqlmock.NewRows([]string{"schema_version", "payload", "state", "result", "request_fingerprint"}).AddRow(1, payload, "pending", nil, cmd.RequestFingerprint))
	mock.ExpectQuery("INSERT INTO usage_billing_dedup").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery("SELECT request_fingerprint.*FROM usage_billing_dedup_archive").WillReturnRows(sqlmock.NewRows([]string{"request_fingerprint"}))
	mock.ExpectQuery("SELECT EXISTS.*FROM usage_logs").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(conditionalBalanceDeductSQL).WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(99))
	mock.ExpectQuery("SELECT balance::text,status FROM users").WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"balance", "status"}).AddRow("99.00000000", "active"))
	mock.ExpectQuery("UPDATE auto_credit_config SET debit_epoch").WillReturnRows(sqlmock.NewRows([]string{"debit_epoch"}))
	mock.ExpectExec("INSERT INTO usage_logs").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE usage_billing_events SET state='applied'").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errors.New("lost commit response"))
	mock.ExpectQuery("SELECT state,result FROM usage_billing_events").WillReturnRows(sqlmock.NewRows([]string{"state", "result"}).AddRow("applied", storedResult))
	recovered, err := (&usageBillingRepository{db: db}).ApplyStagedUsageBilling(context.Background(), service.UsageBillingEventKey{RequestID: "unknown", APIKeyID: 2})
	require.NoError(t, err)
	require.True(t, recovered.NeedsCache)
	require.NoError(t, mock.ExpectationsWereMet(), "only one money UPDATE is allowed")
}

func TestUsageBillingStageCommitUnknownChecksStoredCommand(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	cmd := service.UsageBillingCommand{RequestID: "stage-unknown", APIKeyID: 2, UserID: 1, AccountID: 3}
	usage := service.UsageLog{RequestID: cmd.RequestID, APIKeyID: 2, UserID: 1, AccountID: 3, CreatedAt: time.Now()}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO usage_billing_events").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT schema_version=1 AND request_fingerprint").WillReturnRows(sqlmock.NewRows([]string{"same"}).AddRow(true))
	mock.ExpectCommit().WillReturnError(errors.New("stage commit response lost"))
	mock.ExpectQuery("SELECT schema_version=1 AND request_fingerprint").WillReturnRows(sqlmock.NewRows([]string{"same"}).AddRow(true))
	require.NoError(t, (&usageBillingRepository{db: db}).StageUsageBilling(context.Background(), &cmd, &usage))
	require.NoError(t, mock.ExpectationsWereMet(), "stage must not execute money effects")
}
