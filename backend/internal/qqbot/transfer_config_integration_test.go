//go:build integration

package qqbot

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTransferConfigPreservesUnrelatedSettingsAndRejectsInvalidUpdates(t *testing.T) {
	ctx := context.Background()
	s := savedBot(t, testDB(t))
	before, err := s.Config(ctx)
	require.NoError(t, err)
	want := TransferConfig{TransferEnabled: true, TransferFeePercent: 2.5, TransferMinAmount: 0.1, TransferMaxAmount: 100}
	require.NoError(t, s.SaveTransferConfig(ctx, want))
	got, err := s.TransferConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, want, got)
	after, err := s.Config(ctx)
	require.NoError(t, err)
	before.TransferEnabled = want.TransferEnabled
	before.TransferFeePercent = want.TransferFeePercent
	before.TransferMinAmount = want.TransferMinAmount
	before.TransferMaxAmount = want.TransferMaxAmount
	require.Equal(t, before, after)
	for _, invalid := range []TransferConfig{
		{TransferEnabled: true, TransferFeePercent: 101},
		{TransferEnabled: true, TransferMinAmount: -1},
		{TransferEnabled: true, TransferMaxAmount: -1},
		{TransferEnabled: true, TransferMinAmount: 10, TransferMaxAmount: 1},
	} {
		require.Error(t, s.SaveTransferConfig(ctx, invalid))
		got, err = s.TransferConfig(ctx)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	want.TransferMaxAmount = 0
	require.NoError(t, s.SaveTransferConfig(ctx, want))
	got, err = s.TransferConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestBasicConfigDoesNotOverwriteNewerTransferSettings(t *testing.T) {
	ctx := context.Background()
	s := savedBot(t, testDB(t))
	stale, err := s.Config(ctx)
	require.NoError(t, err)
	want := TransferConfig{TransferEnabled: true, TransferFeePercent: 3, TransferMinAmount: 1, TransferMaxAmount: 50}
	require.NoError(t, s.SaveTransferConfig(ctx, want))
	stale.SystemPrompt = "updated prompt"
	_, err = s.SaveBasicConfig(ctx, stale)
	require.NoError(t, err)
	got, err := s.TransferConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, want, got)
}
