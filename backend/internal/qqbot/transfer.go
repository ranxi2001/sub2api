package qqbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

var ErrTransferInvalid = errors.New("转账链接无效、已过期或已使用，请重新发起转账")

const transferExpiry = 15 * time.Minute

const transferUsage = "用法：转账 金额 收款方邮箱"

type preparedTransferEmail struct {
	recipient string // sender's email — they must click to confirm
	token     string
	link      string
	amount    decimal.Decimal
	fee       decimal.Decimal
	netAmount decimal.Decimal
	toEmail   string
}

// TransferPreview holds amounts as strings to avoid float representation issues in JSON.
type TransferPreview struct {
	FromEmail string    `json:"from_email"`
	ToEmail   string    `json:"to_email"`
	Amount    string    `json:"amount"`
	Fee       string    `json:"fee"`
	NetAmount string    `json:"net_amount"`
	ExpiresAt time.Time `json:"expires_at"`
}

func transferURL(base, token string) (string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if err := validateURL(base); err != nil {
		return "", err
	}
	u, _ := url.Parse(base)
	u.Path += "/qqbot/transfer"
	// Fragment keeps the token out of HTTP access logs and Referer headers.
	u.Fragment = "token=" + token
	return u.String(), nil
}

func transferEmailBody(link string, amount, fee, netAmount decimal.Decimal, toEmail string) string {
	feeNote := ""
	if fee.IsPositive() {
		feeNote = fmt.Sprintf("<p>手续费：$%s<br>对方实际到账：$%s</p>",
			fee.StringFixed(4), netAmount.StringFixed(4))
	}
	return fmt.Sprintf(`<!doctype html><html><body style="font-family:sans-serif;line-height:1.7;max-width:600px;margin:32px auto;padding:16px">
<h2>确认账户余额转账</h2>
<p>你在 QQ 群中发起了一笔转账申请：</p>
<p>转出金额：<strong>$%s</strong><br>收款方邮箱：%s</p>
%s<p><a href="%s">查看并确认转账</a></p>
<p>链接 15 分钟内有效，只能使用一次。打开页面后还需点击确认，不需要登录网站。</p>
<p>仅在你本人发起操作时确认。不要转发链接；如果不是你发起，请忽略这封邮件，账户不会有任何变动。</p>
</body></html>`,
		amount.StringFixed(4), html.EscapeString(toEmail), feeNote, html.EscapeString(link))
}

func transferNotifyBody(fromEmail string, amount, netAmount decimal.Decimal) string {
	return fmt.Sprintf(`<!doctype html><html><body style="font-family:sans-serif;line-height:1.7;max-width:600px;margin:32px auto;padding:16px">
<h2>你收到了一笔余额转账</h2>
<p>来自：%s<br>到账金额：<strong>$%s</strong></p>
<p>已自动计入你的账户余额，无需任何操作。</p>
</body></html>`,
		html.EscapeString(fromEmail), netAmount.StringFixed(4))
}

// parseTransferArgs parses "转账 <amount> <email>" from the command arg.
func parseTransferArgs(arg string) (decimal.Decimal, string, error) {
	parts := strings.Fields(arg)
	if len(parts) != 2 {
		return decimal.Zero, "", errors.New(transferUsage)
	}
	amount, err := decimal.NewFromString(parts[0])
	if err != nil || !amount.IsPositive() {
		return decimal.Zero, "", errors.New("金额格式不正确，请输入正数。")
	}
	// Truncate to 4 decimal places — consistent with the database column precision.
	amount = amount.Truncate(4)
	email := parts[1]
	if !validBindingEmail(email) {
		return decimal.Zero, "", errors.New("邮箱格式不正确。")
	}
	return amount, email, nil
}

func calcFee(amount decimal.Decimal, feePercent float64) (fee, net decimal.Decimal) {
	if feePercent <= 0 {
		return decimal.Zero, amount
	}
	pct := decimal.NewFromFloat(feePercent).Div(decimal.NewFromInt(100))
	fee = amount.Mul(pct).Truncate(4)
	net = amount.Sub(fee)
	return
}

// prepareTransfer validates and inserts a pending transfer record; delivers
// nothing to QQ itself — the caller must send the QQ reply first and then call
// deliverTransferEmail so that SMTP cannot delay the acknowledgement.
func (s *Service) prepareTransfer(ctx context.Context, c Config, e Event, arg string) (string, *preparedTransferEmail) {
	if !c.TransferEnabled {
		return "转账功能未启用。", nil
	}
	if s.bindingEmail == nil {
		return "转账功能需要邮件服务，当前未配置。", nil
	}
	amount, toEmail, err := parseTransferArgs(arg)
	if err != nil {
		return err.Error(), nil
	}
	minAmt := decimal.NewFromFloat(c.TransferMinAmount)
	if !minAmt.IsPositive() {
		minAmt = decimal.NewFromFloat(0.01)
	}
	if amount.LessThan(minAmt) {
		return fmt.Sprintf("最小转账金额为 **USD %s**。", minAmt.StringFixed(4)), nil
	}
	if c.TransferMaxAmount > 0 {
		maxAmt := decimal.NewFromFloat(c.TransferMaxAmount)
		if amount.GreaterThan(maxAmt) {
			return fmt.Sprintf("单笔转账上限为 **USD %s**。", maxAmt.StringFixed(4)), nil
		}
	}

	// Look up sender's bound account — must be active.
	var fromUID int64
	var fromEmail string
	err = s.db.QueryRowContext(ctx,
		`SELECT b.user_id, u.email FROM qq_bot_bindings b
		 JOIN users u ON u.id=b.user_id
		 WHERE b.app_id=$1 AND b.member_id=$2
		   AND u.status='active' AND u.deleted_at IS NULL`,
		c.AppID, e.Author.MemberID).Scan(&fromUID, &fromEmail)
	if errors.Is(err, sql.ErrNoRows) {
		return "请先发送“绑定 邮箱或绑定码”绑定网站账号。", nil
	}
	if err != nil {
		return "账户信息查询失败，请稍后重试。", nil
	}

	// Sender must have a verified email (the confirmation link goes there).
	if !validBindingEmail(fromEmail) {
		return "你的账号邮箱无效，无法发起转账。", nil
	}

	// Recipient lookup by email — the uid/email mapping is re-verified inside the
	// transaction below to guard against stale LookupAccount caches.
	toUID, toEmailVerified, err := s.bindingEmail.LookupAccount(ctx, toEmail)
	if err != nil || toUID <= 0 || !validBindingEmail(toEmailVerified) {
		// Don't distinguish unknown vs known addresses — less information leakage.
		return "收款方邮箱未找到对应账号，请确认邮箱是否正确。", nil
	}
	if toUID == fromUID {
		return "不能向自己转账。", nil
	}

	fee, netAmount := calcFee(amount, c.TransferFeePercent)

	token := randomID() + randomID()
	link, err := transferURL(s.bindingEmail.FrontendURL(ctx), token)
	if err != nil {
		return "转账功能暂不可用，请稍后重试。", nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "转账申请失败，请稍后重试。", nil
	}
	defer func() { _ = tx.Rollback() }()

	// Advisory lock scoped to this app serializes balance checks and token writes.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,274))`, c.AppID); err != nil {
		return "转账申请失败，请稍后重试。", nil
	}

	// Re-read balance as a string to avoid float64 precision loss.
	var balanceStr string
	err = tx.QueryRowContext(ctx,
		`SELECT u.balance::text FROM users u
		 JOIN qq_bot_bindings b ON b.user_id=u.id
		 WHERE b.app_id=$1 AND b.member_id=$2
		   AND u.status='active' AND u.deleted_at IS NULL
		 FOR NO KEY UPDATE OF u`,
		c.AppID, e.Author.MemberID).Scan(&balanceStr)
	if err != nil {
		return "账户信息查询失败，请稍后重试。", nil
	}
	balance, _ := decimal.NewFromString(balanceStr)
	if balance.LessThan(amount) {
		return fmt.Sprintf("余额不足：当前 USD %s，需 USD %s",
			balance.StringFixed(4), amount.StringFixed(4)), nil
	}

	// Re-verify recipient under the transaction lock to guard against LookupAccount
	// returning a stale uid that no longer matches toEmailVerified.
	var confirmedToUID int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM users WHERE id=$1 AND email=$2 AND status='active' AND deleted_at IS NULL`,
		toUID, toEmailVerified).Scan(&confirmedToUID)
	if err != nil || confirmedToUID != toUID {
		return "收款方账号不存在或已注销。", nil
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO qq_bot_transfer_requests
		   (token_hash,app_id,from_member_id,from_user_id,from_email,to_user_id,to_email,amount,fee,net_amount,expires_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW()+$11::interval)
		 ON CONFLICT(app_id,from_member_id) DO UPDATE SET
		   token_hash=EXCLUDED.token_hash, from_user_id=EXCLUDED.from_user_id,
		   from_email=EXCLUDED.from_email, to_user_id=EXCLUDED.to_user_id,
		   to_email=EXCLUDED.to_email, amount=EXCLUDED.amount,
		   fee=EXCLUDED.fee, net_amount=EXCLUDED.net_amount,
		   expires_at=EXCLUDED.expires_at, created_at=NOW()`,
		codeHash(token), c.AppID, e.Author.MemberID, fromUID, fromEmail,
		toUID, toEmailVerified, amount.StringFixed(8), fee.StringFixed(8), netAmount.StringFixed(8),
		fmt.Sprintf("%d seconds", int(transferExpiry.Seconds())))
	if err != nil {
		return "转账申请失败，请稍后重试。", nil
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return "转账申请失败，请稍后重试。", nil
	}
	if err = tx.Commit(); err != nil {
		return "转账申请失败，请稍后重试。", nil
	}

	message := fmt.Sprintf("✉️ **转账申请已受理**\n转账金额：USD %s\n手续费：%s%%（USD %s）\n对方到账：USD %s\n\n15 分钟内邮件确认后扣款。\n手续费从转账金额中扣除。",
		amount.StringFixed(4), decimal.NewFromFloat(c.TransferFeePercent).String(), fee.StringFixed(4), netAmount.StringFixed(4))
	return message, &preparedTransferEmail{
		recipient: fromEmail,
		token:     token,
		link:      link,
		amount:    amount,
		fee:       fee,
		netAmount: netAmount,
		toEmail:   toEmailVerified,
	}
}

func (s *Service) deliverTransferEmail(ctx context.Context, pending *preparedTransferEmail) {
	body := transferEmailBody(pending.link, pending.amount, pending.fee, pending.netAmount, pending.toEmail)
	if err := s.bindingEmail.SendEmail(ctx, pending.recipient, "确认账户余额转账", body); err != nil {
		// SMTP failure: clean up so the sender can retry immediately.
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = s.db.ExecContext(cleanup,
			`DELETE FROM qq_bot_transfer_requests WHERE token_hash=$1`, codeHash(pending.token))
	}
}

func validTransferToken(token string) bool { return validBindingToken(token) }

// PreviewTransfer returns details of a pending transfer without consuming the token.
func (s *Service) PreviewTransfer(ctx context.Context, token string) (TransferPreview, error) {
	var p TransferPreview
	if !validTransferToken(token) {
		return p, ErrTransferInvalid
	}
	c, err := s.Config(ctx)
	if err != nil {
		return p, err
	}
	if !c.Enabled || !c.TransferEnabled {
		return p, ErrTransferInvalid
	}
	var amount, fee, netAmount string
	err = s.db.QueryRowContext(ctx,
		`SELECT r.from_email, r.to_email,
		        r.amount::text, r.fee::text, r.net_amount::text, r.expires_at
		 FROM qq_bot_transfer_requests r
		 WHERE r.token_hash=$1 AND r.app_id=$2 AND r.expires_at>NOW()`,
		codeHash(token), c.AppID).Scan(
		&p.FromEmail, &p.ToEmail, &amount, &fee, &netAmount, &p.ExpiresAt)
	if err != nil {
		return TransferPreview{}, ErrTransferInvalid
	}
	p.Amount = amount
	p.Fee = fee
	p.NetAmount = netAmount
	return p, nil
}

// ConfirmTransfer executes the transfer atomically after the sender clicks the link.
// On success it sends a notification email to the recipient (non-critical).
func (s *Service) ConfirmTransfer(ctx context.Context, token string) error {
	if !validTransferToken(token) {
		return ErrTransferInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	c, err := s.lockConfig(ctx, tx)
	if err != nil {
		return err
	}
	if !c.Enabled || !c.TransferEnabled {
		return ErrTransferInvalid
	}

	// Serialize concurrent confirmations for the same app.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,274))`, c.AppID); err != nil {
		return err
	}

	var fromUID, toUID int64
	var fromEmail, toEmail string
	var amountStr, netAmountStr string
	var expiresAt time.Time
	// Use clock_timestamp() (real wall-clock, not transaction start) to recheck expiry.
	err = tx.QueryRowContext(ctx,
		`SELECT r.from_user_id, r.to_user_id, r.from_email, r.to_email,
		        r.amount::text, r.net_amount::text, r.expires_at
		 FROM qq_bot_transfer_requests r
		 WHERE r.token_hash=$1 AND r.app_id=$2 AND r.expires_at>clock_timestamp()
		 FOR UPDATE`,
		codeHash(token), c.AppID).Scan(
		&fromUID, &toUID, &fromEmail, &toEmail, &amountStr, &netAmountStr, &expiresAt)
	if err != nil {
		return ErrTransferInvalid
	}

	amount, _ := decimal.NewFromString(amountStr)
	netAmount, _ := decimal.NewFromString(netAmountStr)

	// Verify sender's balance under lock (same clock as database).
	var senderBalanceStr string
	err = tx.QueryRowContext(ctx,
		`SELECT balance::text FROM users
		 WHERE id=$1 AND status='active' AND deleted_at IS NULL FOR NO KEY UPDATE`,
		fromUID).Scan(&senderBalanceStr)
	if err != nil {
		return ErrTransferInvalid
	}
	senderBalance, _ := decimal.NewFromString(senderBalanceStr)
	if senderBalance.LessThan(amount) {
		return errors.New("余额不足，转账已取消。")
	}

	// Verify recipient still exists.
	var recipientExists bool
	_ = tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND status='active' AND deleted_at IS NULL)`,
		toUID).Scan(&recipientExists)
	if !recipientExists {
		return errors.New("收款方账号不存在，转账已取消。")
	}

	// Debit sender, credit recipient using exact decimal strings.
	if _, err = tx.ExecContext(ctx,
		`UPDATE users SET balance=balance-$1,updated_at=NOW()
		 WHERE id=$2 AND status='active' AND deleted_at IS NULL`,
		amount.StringFixed(8), fromUID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE users SET balance=balance+$1,updated_at=NOW()
		 WHERE id=$2 AND status='active' AND deleted_at IS NULL`,
		netAmount.StringFixed(8), toUID); err != nil {
		return err
	}

	// Receipt and balances commit atomically. No token or token hash enters history.
	if _, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_transfer_history(app_id,from_member_id,from_user_id,to_user_id,from_email,to_email,amount,fee,net_amount)
 SELECT app_id,from_member_id,from_user_id,to_user_id,from_email,to_email,amount,fee,net_amount FROM qq_bot_transfer_requests WHERE token_hash=$1`, codeHash(token)); err != nil {
		return err
	}
	// Delete the token — single use.
	if _, err = tx.ExecContext(ctx,
		`DELETE FROM qq_bot_transfer_requests WHERE token_hash=$1`, codeHash(token)); err != nil {
		return err
	}

	if err = tx.Commit(); err != nil {
		return err
	}
	s.invalidate(ctx, fromUID)
	s.invalidate(ctx, toUID)

	// Notify the recipient. Non-critical: the money has already moved.
	if s.bindingEmail != nil && toEmail != "" {
		body := transferNotifyBody(fromEmail, amount, netAmount)
		notify, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.bindingEmail.SendEmail(notify, toEmail, "你收到了一笔余额转账", body)
	}

	return nil
}
