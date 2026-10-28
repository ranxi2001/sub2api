package repository

import (
	"context"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestUpstreamOperationsQueryFailureCannotBeSuccessfulZero(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectBegin().WillReturnError(errors.New("synthetic database failure"))
	now := time.Now()
	q := service.UpstreamOperationsFilter{From: now.Add(-time.Hour), Until: now, Dimension: "upstream", ModelScope: "public", Sort: "requests", Direction: "desc", Page: 1, PageSize: 20}
	out, err := (&channelRepository{db: db}).ReadUpstreamOperations(context.Background(), q)
	require.Error(t, err)
	require.Nil(t, out, "a database failure must not become an empty successful snapshot")
	require.NoError(t, mock.ExpectationsWereMet())
}
