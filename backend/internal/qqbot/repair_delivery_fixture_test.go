package qqbot

import (
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

type repairDeliveryFixtureEncryptor struct{}

func (repairDeliveryFixtureEncryptor) Encrypt(v string) (string, error) { return v, nil }
func (repairDeliveryFixtureEncryptor) Decrypt(v string) (string, error) { return v, nil }

// Protocol-only tests supply a committed authorization snapshot and expect the
// same transaction/lock sequence as production. Revocation/races use real PG.
func expectRepairDelivery(t *testing.T, s *Service, c Config, requests int) Config {
	t.Helper()
	c.Enabled = true
	c.AllowedGroups = []string{"group", "other"}
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	s.db = db
	s.encryptor = repairDeliveryFixtureEncryptor{}
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()); _ = db.Close() })
	expectRepairDeliverySnapshot(t, mock, c, requests)
	return c
}

func expectRepairDeliverySnapshot(t *testing.T, mock sqlmock.Sqlmock, c Config, requests int) {
	t.Helper()
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	for range requests {
		mock.ExpectBegin()
		mock.ExpectExec("SELECT pg_advisory_xact_lock_shared").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("SELECT value FROM settings").WithArgs(configKey).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(string(raw)))
		mock.ExpectRollback()
	}
}
