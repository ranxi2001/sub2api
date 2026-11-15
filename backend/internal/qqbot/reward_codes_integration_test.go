//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestHistoricalRewardRecordsRemainManageable(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	for i, kind := range []string{"physical", "other", "balance", "chances"} {
		status := "fulfilled"
		amount, quantity := 0., 2
		if kind == "balance" {
			amount = 10
			quantity = 0
		}
		if kind == "physical" || kind == "other" {
			status = "pending"
		}
		_, err := db.Exec(`INSERT INTO qq_bot_reward_codes(app_id,code_hash,code_hint,batch_name,reward_type,amount,quantity,prize_name,status,user_id,redeemed_at) VALUES('app',$1,'ABCDEF','historical',$2,$3,$4,'cup',$5,$6,NOW())`, fmt.Sprintf("hash-%d", i), kind, amount, quantity, status, uid)
		require.NoError(t, err)
	}
	_, err := db.Exec(`INSERT INTO qq_bot_reward_codes(app_id,code_hash,code_hint,batch_name,reward_type,amount,status) VALUES('old-app','unused-hash','UNUSED','historical','balance',1,'unused')`)
	require.NoError(t, err)
	page, err := s.ListRewardCodes(ctx, "historical", "", 1)
	require.NoError(t, err)
	require.Equal(t, 5, page.Total)
	for _, record := range page.Items {
		require.Equal(t, "code", record.SourceKind)
		switch record.Status {
		case "pending":
			require.NoError(t, s.UpdateRewardCode(ctx, record.ID, "fulfill", "Delivered privately"))
			require.ErrorIs(t, s.UpdateRewardCode(ctx, record.ID, "fulfill", "again"), ErrRewardCodeRequest)
		case "unused":
			require.NoError(t, s.UpdateRewardCode(ctx, record.ID, "disable", ""))
		default:
			require.ErrorIs(t, s.UpdateRewardCode(ctx, record.ID, "fulfill", "again"), ErrRewardCodeRequest)
		}
	}
	page, err = s.ListRewardCodes(ctx, "historical", "pending", 1)
	require.NoError(t, err)
	require.Zero(t, page.Total)
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Zero(t, balance)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM redeem_codes`).Scan(&count))
	require.Zero(t, count)
}

func TestRetiredRedemptionWebhookNeverGrantsOrPersistsCode(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	c, err := s.Config(ctx)
	require.NoError(t, err)
	sender, replies := helpTestSender(t)
	s.qqURL, s.tokenURL = sender.qqURL, sender.tokenURL
	code := "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"
	for i, cmd := range []string{"/福利 兑换 ", "/奖励兑换 "} {
		e := Event{ID: fmt.Sprint(i), GroupID: "group", Content: cmd + code, MsgElements: []MessageElement{{Content: code}}}
		e.Author.MemberID = "member"
		raw, err := json.Marshal(e)
		require.NoError(t, err)
		for _, event := range []string{groupMessageCreate, groupAtMessageCreate} {
			require.NoError(t, s.Enqueue(ctx, c, Payload{ID: event + fmt.Sprint(i), Type: event, Data: raw}))
		}
	}
	rows, err := db.Query(`SELECT payload::text,reward_code_hash FROM qq_bot_jobs`)
	require.NoError(t, err)
	count := 0
	for rows.Next() {
		var payload, hash string
		require.NoError(t, rows.Scan(&payload, &hash))
		require.NotContains(t, payload, code)
		require.Contains(t, payload, "content")
		require.Empty(t, hash)
		count++
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, 2, count)
	s.runOne(ctx)
	s.runOne(ctx)
	require.Len(t, *replies, 2)
	for _, reply := range *replies {
		require.NotContains(t, reply.text(), code)
		require.Contains(t, reply.text(), "帮助")
	}
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_reward_codes`).Scan(&count))
	require.Zero(t, count)
}
