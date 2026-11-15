package qqbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const bindFirst = "请先发送“绑定 邮箱或绑定码”绑定网站账号。"

func formatUsageTokens(tokens int64) string {
	switch {
	case tokens >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(tokens)/1_000_000_000)
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(tokens)/1_000_000)
	case tokens >= 1_000:
		return fmt.Sprintf("%.2fK", float64(tokens)/1_000)
	default:
		return strconv.FormatInt(tokens, 10)
	}
}

func boundUser(ctx context.Context, db queryRower, app, member string) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, `SELECT b.user_id FROM qq_bot_bindings b JOIN users u ON u.id=b.user_id WHERE b.app_id=$1 AND b.member_id=$2 AND u.status='active' AND u.deleted_at IS NULL`, app, member).Scan(&id)
	return id, err
}

func (s *Service) todayUsage(ctx context.Context, c Config, j Job, now time.Time) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	uid, err := boundUser(ctx, tx, c.AppID, j.MemberID)
	if errors.Is(err, sql.ErrNoRows) {
		return bindFirst, nil
	}
	if err != nil {
		return "", err
	}
	local := now.In(time.FixedZone("Asia/Shanghai", 8*3600))
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	var count, input, output, write, read int64
	var cost, wallet, subscription float64
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(cache_creation_tokens),0),COALESCE(SUM(cache_read_tokens),0),COALESCE(SUM(actual_cost),0),COALESCE(SUM(actual_cost) FILTER(WHERE billing_type=0),0),COALESCE(SUM(actual_cost) FILTER(WHERE billing_type=1),0) FROM usage_logs WHERE user_id=$1 AND created_at >= $2 AND created_at <= $3`, uid, start, now).Scan(&count, &input, &output, &write, &read, &cost, &wallet, &subscription)
	if err != nil {
		return "", err
	}
	text := fmt.Sprintf("📊 **今日用量**\n%s（北京时间）\n\n请求：%d 次\n费用：USD %.4f\n余额扣费：USD %.4f\n套餐消耗：USD %.4f\n\n**Token 明细**\n输入：%s\n输出：%s\n缓存读取：%s\n缓存写入：%s", local.Format("2006-01-02"), count, cost, wallet, subscription, formatUsageTokens(input), formatUsageTokens(output), formatUsageTokens(read), formatUsageTokens(write))
	if count == 0 {
		return text, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT model,COUNT(*),SUM(actual_cost) FROM usage_logs WHERE user_id=$1 AND created_at >= $2 AND created_at <= $3 GROUP BY model ORDER BY SUM(actual_cost) DESC,COUNT(*) DESC,model LIMIT 5`, uid, start, now)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	text += "\n\n**费用前 5 模型**"
	rank := 0
	for rows.Next() {
		var model string
		var requests int64
		var amount float64
		if err = rows.Scan(&model, &requests, &amount); err != nil {
			return "", err
		}
		rank++
		text += fmt.Sprintf("\n\n**%d. %s**\nUSD %.4f（%d 次）", rank, replyPlainLabel(limitText(model, 100)), amount, requests)
	}
	return text, rows.Err()
}

type ModelPriceProvider interface {
	ModelPrice(context.Context, string, int64, time.Time) ([]string, error)
}

func (s *Service) SetModelPriceProvider(p ModelPriceProvider) { s.modelPrices = p }

func (s *Service) sendModelPrice(ctx context.Context, c Config, j Job, arg string) error {
	parts := strings.Fields(arg)
	if len(parts) < 1 || len(parts) > 2 || len(parts[0]) > 200 {
		return s.send(ctx, c, j, "用法：价格 模型名 [页码]", nil, 1)
	}
	page := 1
	if len(parts) == 2 {
		var err error
		page, err = strconv.Atoi(parts[1])
		if err != nil || page < 1 || strconv.Itoa(page) != parts[1] {
			return s.send(ctx, c, j, "页码须为正整数。", nil, 1)
		}
	}
	uid, err := boundUser(ctx, s.db, c.AppID, j.MemberID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return s.send(ctx, c, j, "价格查询失败，请稍后重试。", nil, 1)
	}
	if s.modelPrices == nil {
		return s.send(ctx, c, j, "价格查询暂时不可用。", nil, 1)
	}
	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	blocks, err := s.modelPrices.ModelPrice(queryCtx, parts[0], uid, time.Now())
	if err != nil {
		return s.send(ctx, c, j, "价格查询失败，请稍后重试。", nil, 1)
	}
	if len(blocks) == 0 {
		return s.send(ctx, c, j, "未找到该模型价格，请核对名称或查看网站。", nil, 1)
	}
	// One complete group per message; do not truncate pricing tiers.
	pages := (len(blocks) + 4) / 5
	if page > pages {
		return s.send(ctx, c, j, fmt.Sprintf("价格查询共 %d 页。", pages), nil, 1)
	}
	for i := (page - 1) * 5; i < min(page*5, len(blocks)); i++ {
		body := blocks[i]
		if pages > 1 {
			body += fmt.Sprintf("\n\n第 %d/%d 页", page, pages)
			if page < pages {
				body += fmt.Sprintf("\n下一页：价格 %s %d", replyPlainLabel(parts[0]), page+1)
			}
		}
		if err = s.send(ctx, c, j, body, nil, i-(page-1)*5+1); err != nil {
			return err
		}
	}
	return nil
}
