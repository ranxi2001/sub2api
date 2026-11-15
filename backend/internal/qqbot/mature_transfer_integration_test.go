//go:build integration

package qqbot

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMatureTransferRollsBackWhenRecipientIsNotCredited(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	raw, err := migrations.FS.ReadFile("355_qq_bot_transfer_requests.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(raw))
	require.NoError(t, err)
	require.NoError(t, s.SaveTransferConfig(ctx, TransferConfig{TransferEnabled: true, TransferMinAmount: 1, TransferMaxAmount: 100}))
	from, to := user(t, db), user(t, db)
	_, err = db.Exec(`UPDATE users SET balance=10 WHERE id=$1`, from)
	require.NoError(t, err)
	token := strings.Repeat("a", 48)
	_, err = db.Exec(`INSERT INTO qq_bot_transfer_requests(token_hash,app_id,from_member_id,from_user_id,from_email,to_user_id,to_email,amount,fee,net_amount,expires_at) VALUES($1,'app','member',$2,'from@example.invalid',$3,'to@example.invalid',5,0,5,NOW()+INTERVAL '5 minutes')`, codeHash(token), from, to)
	require.NoError(t, err)
	// A rejected recipient update must never debit the sender or issue a
	// receipt. The trigger deterministically exercises zero affected rows.
	_, err = db.Exec(fmt.Sprintf(`CREATE FUNCTION reject_recipient_credit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.id=%d THEN RETURN NULL; END IF; RETURN NEW; END$$;
 CREATE TRIGGER reject_recipient_credit BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION reject_recipient_credit()`, to))
	require.NoError(t, err)
	require.Error(t, s.ConfirmTransfer(ctx, token), "a transfer must roll back unless the recipient is credited")
	var balance string
	require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=$1`, from).Scan(&balance))
	require.Equal(t, "10.00000000", balance)
	var receipts int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_transfer_history`).Scan(&receipts))
	require.Zero(t, receipts)
}
