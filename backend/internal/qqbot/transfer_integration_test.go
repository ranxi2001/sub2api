//go:build integration

package qqbot

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestTransferAcknowledgementShowsPendingAmounts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		amount     string
		feePercent float64
		feeLine    string
		fee        string
		netAmount  string
	}{
		{"with_fee", "5.0000", 1, "手续费：1%（USD 0.0500）", "0.0500", "4.9500"},
		{"without_fee", "5.0000", 0, "手续费：0%（USD 0.0000）", "0.0000", "5.0000"},
		{"fractional_fee_truncated", "1.2345", 2.5, "手续费：2.5%（USD 0.0308）", "0.0308", "1.2037"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, mail, c, e, fromUID := emailFixture(t)
			raw, err := migrations.FS.ReadFile("355_qq_bot_transfer_requests.sql")
			require.NoError(t, err)
			_, err = s.db.ExecContext(ctx, string(raw))
			require.NoError(t, err)
			bind(t, s.db, fromUID, e.Author.MemberID)
			_, err = s.db.ExecContext(ctx, `UPDATE users SET balance=10 WHERE id=$1`, fromUID)
			require.NoError(t, err)
			toUID := user(t, s.db)
			_, err = s.db.ExecContext(ctx, `UPDATE users SET email='recipient@example.com' WHERE id=$1`, toUID)
			require.NoError(t, err)
			c.TransferEnabled, c.TransferFeePercent = true, tc.feePercent

			reply, pending := s.prepareTransfer(ctx, c, e, tc.amount+" recipient@example.com")
			require.NotNil(t, pending, reply)
			require.Contains(t, reply, "转账申请已受理")
			require.Contains(t, reply, "转账金额：USD "+tc.amount)
			require.Contains(t, reply, tc.feeLine)
			require.Contains(t, reply, "对方到账：USD "+tc.netAmount)
			require.Contains(t, reply, "手续费从转账金额中扣除")
			require.Contains(t, reply, "15 分钟内邮件确认后扣款")
			require.NotContains(t, reply, pending.token)
			require.NotContains(t, reply, pending.recipient)
			require.NotContains(t, reply, pending.toEmail)
			require.NotContains(t, reply, "邮件已发送")
			require.Equal(t, tc.amount, pending.amount.StringFixed(4))
			require.Equal(t, tc.fee, pending.fee.StringFixed(4))
			require.Equal(t, tc.netAmount, pending.netAmount.StringFixed(4))
			require.Empty(t, mail.bodies, "preparing the acknowledgement must not send email")

			var amount, fee, netAmount string
			require.NoError(t, s.db.QueryRowContext(ctx,
				`SELECT amount::text,fee::text,net_amount::text FROM qq_bot_transfer_requests WHERE token_hash=$1`,
				codeHash(pending.token)).Scan(&amount, &fee, &netAmount))
			require.Equal(t, tc.amount, decimal.RequireFromString(amount).StringFixed(4))
			require.Equal(t, tc.fee, decimal.RequireFromString(fee).StringFixed(4))
			require.Equal(t, tc.netAmount, decimal.RequireFromString(netAmount).StringFixed(4))
			for uid, expected := range map[int64]string{fromUID: "10.0000", toUID: "0.0000"} {
				var balance string
				require.NoError(t, s.db.QueryRowContext(ctx, `SELECT balance::text FROM users WHERE id=$1`, uid).Scan(&balance))
				require.Equal(t, expected, decimal.RequireFromString(balance).StringFixed(4), "balances stay unchanged before confirmation")
			}
		})
	}
}
