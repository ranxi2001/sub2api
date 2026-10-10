package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"math"
	"strings"
	"time"
)

type accountOpsMessage struct {
	title, robotTitle, plain, markdown, html, color string
	account, balance, threshold, observedTime       string
}

func (s *AccountOpsService) notificationMessage(e *AccountOpsEvent) accountOpsMessage {
	reason := "上游余额不足"
	action := "请检查账号的上游余额并充值。"
	if e.Kind == "weekly_quota" {
		reason = "上游周额度已用尽"
		action = "请检查上游周额度及恢复时间。"
	}
	if e.Kind == "balance_threshold" {
		reason = "余额达到提醒阈值"
	}
	if e.Kind == "quota_threshold" {
		reason = "OAuth 用量达到提醒阈值"
		action = "请检查对应额度窗口和重置时间。"
	}
	if e.Phase == "recovery" {
		if e.Kind == "balance_threshold" {
			reason = "余额已确认恢复"
		} else {
			reason = "OAuth 用量已确认恢复"
		}
		action = "本记录表示恢复时的有效数据，请以最新账号数据判断当前状态。"
	}
	accountLabel := e.AccountName
	if e.GroupName != "" && !strings.HasPrefix(e.GroupID, "account:") {
		accountLabel = e.GroupName
	}
	fields := [][2]string{{"账号", fmt.Sprintf("%s（#%d）", accountLabel, e.AccountID)}, {"提醒原因", reason}}
	if e.Details != nil {
		d := e.Details
		if d.UsedPercent != nil {
			label := "已使用额度"
			if e.Phase == "recovery" {
				label = "恢复时用量"
			}
			fields = append(fields, [2]string{label, fmt.Sprintf("%g%%（%s）", *d.UsedPercent, d.Window)})
		}
		if d.ThresholdPercent != nil {
			fields = append(fields, [2]string{"用量提醒阈值", fmt.Sprintf("%g%%", *d.ThresholdPercent)})
		}
		if d.ResetsAt != nil {
			fields = append(fields, [2]string{"额度重置时间", d.ResetsAt.In(s.timezone).Format("2006-01-02 15:04:05 MST")})
		}
		if d.Balance != nil {
			label := "检测时余额"
			if e.Phase == "recovery" {
				label = "恢复时余额"
			}
			fields = append(fields, [2]string{label, fmt.Sprintf("%g %s", *d.Balance, d.Unit)})
		}
		if d.Threshold != nil {
			fields = append(fields, [2]string{"提醒阈值", fmt.Sprintf("%g %s", *d.Threshold, d.Unit)})
		}
		if d.ObservedAt != nil {
			fields = append(fields, [2]string{"数据更新时间", d.ObservedAt.In(s.timezone).Format("2006-01-02 15:04:05 MST")})
		}
	}
	if e.Kind == "balance_low" || e.Kind == "weekly_quota" {
		fields = append(fields, [2]string{"上游状态", fmt.Sprintf("HTTP %d", e.HTTPStatus)}, [2]string{"识别信号", accountOpsSignalLabel(e.Signal)})
	}
	fields = append(fields, [2]string{"检测时间", e.LastSeen.In(s.timezone).Format("2006-01-02 15:04:05 MST")}, [2]string{"累计触发", fmt.Sprintf("%d 次", e.Occurrences)}, [2]string{"建议操作", action})
	title := reason
	body := "<h2>" + html.EscapeString(title) + "</h2><table style=\"border-collapse:collapse\">"
	for _, f := range fields {
		body += "<tr><th style=\"text-align:left;padding:8px\">" + html.EscapeString(f[0]) + "</th><td style=\"padding:8px\">" + html.EscapeString(f[1]) + "</td></tr>"
	}
	body += "</table>"
	if e.Kind != "balance_threshold" && e.Kind != "quota_threshold" {
		body += "<p>此提醒基于上游失败响应，不代表已查询到准确余额。</p>"
	}
	robotReason := "上游账户余额不足"
	switch e.Kind {
	case "weekly_quota":
		robotReason = "上游账户周额度已用尽"
	case "quota_threshold":
		robotReason = "上游账户额度不足"
	}
	if e.Phase == "recovery" {
		robotReason = "上游账户余额已恢复"
		if e.Kind == "quota_threshold" {
			robotReason = "上游账户额度已恢复"
		}
	}
	message := s.compactRobotMessage(e, robotReason)
	message.title, message.html = title, body
	return message
}

func (s *AccountOpsService) compactRobotMessage(e *AccountOpsEvent, title string) accountOpsMessage {
	account := strings.Join(strings.Fields(e.AccountName), " ")
	if e.GroupName != "" && !strings.HasPrefix(e.GroupID, "account:") {
		account = strings.Join(strings.Fields(e.GroupName), " ")
	}
	if account == "" {
		account = "未命名账号"
	}
	if e.AccountID > 0 {
		account = fmt.Sprintf("%s (#%d)", account, e.AccountID)
	}
	balance, threshold := "未获取", "未获取"
	if e.Kind == "balance_low" || e.Kind == "weekly_quota" {
		threshold = "不适用"
	}
	if d := e.Details; d != nil {
		if d.UsedPercent != nil {
			balance = fmt.Sprintf("%g%%", math.Max(0, 100-*d.UsedPercent))
			if d.Window != "" {
				balance += "（" + strings.Join(strings.Fields(d.Window), " ") + "）"
			}
		} else if d.Balance != nil {
			balance = opsRobotAmount(*d.Balance, d.Unit)
		}
		if d.ThresholdPercent != nil {
			threshold = fmt.Sprintf("%g%%（已用 %g%%）", math.Max(0, 100-*d.ThresholdPercent), *d.ThresholdPercent)
		} else if d.Threshold != nil {
			threshold = opsRobotAmount(*d.Threshold, d.Unit)
		}
	}
	fields := [][2]string{
		{"账户", account},
		{"余额", balance},
		{"阈值", threshold},
		{"时间", e.LastSeen.In(s.timezone).Format("2006-01-02 15:04:05")},
	}
	plain := make([]string, 0, len(fields))
	markdown := "### " + title
	for _, field := range fields {
		plain = append(plain, field[0]+"："+field[1])
		markdown += "\n**" + field[0] + "**：" + escapeOpsMarkdown(field[1])
	}
	color := "orange"
	if e.Phase == "recovery" {
		color = "green"
	}
	return accountOpsMessage{title: title, robotTitle: title, plain: strings.Join(plain, "\n"), markdown: markdown, color: color, account: account, balance: balance, threshold: threshold, observedTime: fields[3][1]}
}

func opsRobotAmount(value float64, unit string) string {
	unit = strings.Join(strings.Fields(unit), " ")
	if strings.EqualFold(unit, "USD") || unit == "$" {
		return fmt.Sprintf("$%g", value)
	}
	return strings.TrimSpace(fmt.Sprintf("%g %s", value, unit))
}

type accountOpsRobotReservationRepository interface {
	ReserveRobotDelivery(context.Context, *AccountOpsEvent, string, string, string) (bool, error)
}
type accountOpsLeaseRepository interface {
	DeliveryLeaseValid(context.Context, *AccountOpsEvent) (bool, error)
}
type accountOpsDeliveryRepository interface {
	SaveDelivery(context.Context, *AccountOpsEvent, string, AccountOpsDelivery) (bool, error)
	ResolveThreshold(context.Context, int64, string, time.Duration) error
	SuppressThreshold(context.Context, int64, string, time.Duration) error
}

func opsEmailIdentity(recipient string) string {
	h := sha256.Sum256([]byte(strings.ToLower(recipient)))
	return "email:" + hex.EncodeToString(h[:12])
}
func (s *AccountOpsService) deliverNotification(ctx context.Context, e *AccountOpsEvent) {
	query, cancel := context.WithTimeout(ctx, 5*time.Second)
	s.settingsMu.Lock()
	c, err := s.loadConfig(query)
	s.settingsMu.Unlock()
	cancel()
	state := "suppressed"
	delay := time.Hour
	if err == nil {
		delay = time.Duration(c.CooldownMinutes) * time.Minute
	}
	if err != nil {
		state = "failed"
	} else if c.Allows(e.Kind) {
		if e.Kind == "balance_threshold" || e.Kind == "quota_threshold" {
			thresholdState, checkErr := s.recheckThreshold(ctx, e, c)
			if checkErr != nil {
				state = "failed"
			} else if thresholdState != "active" {
				state = thresholdState
			}
			if checkErr != nil || thresholdState != "active" {
				s.finishNotification(ctx, e, state, delay)
				return
			}
		}
		message := s.notificationMessage(e)
		state = "sent"
		if e.Deliveries == nil {
			e.Deliveries = map[string]AccountOpsDelivery{}
		}
		destinations := []AccountOpsWebhook{}
		if c.Recipient != "" {
			destinations = append(destinations, AccountOpsWebhook{ID: opsEmailIdentity(c.Recipient), Provider: "email"})
		}
		for _, w := range c.Webhooks {
			if w.Enabled {
				destinations = append(destinations, w)
			}
		}
		// Six channels at twelve seconds each, including rate waits, remain below the two minute lease.
		deliveryCtx, stop := context.WithTimeout(ctx, 100*time.Second)
		defer stop()
		for _, w := range destinations {
			query, cancel := context.WithTimeout(deliveryCtx, 5*time.Second)
			s.settingsMu.Lock()
			freshConfig, configErr := s.loadConfig(query)
			s.settingsMu.Unlock()
			cancel()
			if configErr != nil {
				s.finishNotification(ctx, e, "failed", delay)
				return
			}
			c = freshConfig
			delay = time.Duration(c.CooldownMinutes) * time.Minute
			if !c.Allows(e.Kind) {
				s.finishNotification(ctx, e, "suppressed", delay)
				return
			}
			if w.Provider == "email" {
				if c.Recipient == "" {
					continue
				}
				w.ID = opsEmailIdentity(c.Recipient)
			} else {
				found := false
				for _, current := range c.Webhooks {
					if current.ID == w.ID && current.Enabled {
						w = current
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}
			key := w.ID
			if w.Provider != "email" {
				key = "robot:" + w.ID + ":" + w.revision
			}
			out := e.Deliveries[key]
			if out.Status == "sent" {
				continue
			}
			if out.Attempts >= 3 {
				state = "failed"
				continue
			}
			if leaseRepo, ok := s.repo.(accountOpsLeaseRepository); ok {
				valid, checkErr := leaseRepo.DeliveryLeaseValid(deliveryCtx, e)
				if checkErr != nil || !valid {
					return
				}
			}
			if e.Kind == "balance_threshold" || e.Kind == "quota_threshold" {
				thresholdState, checkErr := s.recheckThreshold(deliveryCtx, e, c)
				if checkErr != nil {
					s.finishNotification(ctx, e, "failed", delay)
					return
				}
				if thresholdState != "active" {
					s.finishNotification(ctx, e, thresholdState, delay)
					return
				}
				message = s.notificationMessage(e)
			}
			send, cancel := context.WithTimeout(deliveryCtx, 15*time.Second)
			if w.Provider != "email" {
				if reserve, ok := s.repo.(accountOpsRobotReservationRepository); ok {
					identity, identityErr := s.robotIdentity(w)
					if identityErr != nil {
						cancel()
						s.finishNotification(ctx, e, "failed", delay)
						return
					}
					owned, reserveErr := reserve.ReserveRobotDelivery(send, e, key, w.Provider, identity)
					if reserveErr != nil || !owned {
						cancel()
						s.failures.Add(1)
						return
					}
				}
			}
			// A durable rate reservation may wait. Refresh both the destination and
			// the account snapshot after that wait, immediately before the external send.
			s.settingsMu.Lock()
			latest, latestErr := s.loadConfig(send)
			s.settingsMu.Unlock()
			if latestErr != nil {
				cancel()
				s.finishNotification(ctx, e, "failed", delay)
				return
			}
			if !latest.Allows(e.Kind) {
				cancel()
				s.finishNotification(ctx, e, "suppressed", delay)
				return
			}
			unchanged := false
			if w.Provider == "email" {
				unchanged = opsEmailIdentity(latest.Recipient) == key && latest.Recipient != ""
			} else {
				for _, current := range latest.Webhooks {
					if current.ID == w.ID && current.Provider == w.Provider && current.revision == w.revision && current.Enabled {
						w = current
						unchanged = true
						break
					}
				}
			}
			if !unchanged {
				cancel()
				state = "failed"
				continue
			}
			c = latest
			delay = time.Duration(c.CooldownMinutes) * time.Minute
			if e.Kind == "balance_threshold" || e.Kind == "quota_threshold" {
				thresholdState, checkErr := s.recheckThreshold(send, e, c)
				if checkErr != nil {
					cancel()
					s.finishNotification(ctx, e, "failed", delay)
					return
				}
				if thresholdState != "active" {
					cancel()
					s.finishNotification(ctx, e, thresholdState, delay)
					return
				}
				message = s.notificationMessage(e)
			}
			if w.Provider == "email" {
				if s.email == nil {
					err = errors.New("email unavailable")
				} else {
					err = s.email.SendEmail(send, c.Recipient, message.title, message.html)
				}
			} else {
				err = s.sendRobot(send, w, message)
			}
			cancel()
			out.Provider = w.Provider
			out.Name = ""
			if w.Provider == "email" {
				out.Name = c.EmailName
			} else if w.Name != nil {
				out.Name = *w.Name
			}
			out.Attempts++
			out.Status = "sent"
			if err != nil {
				out.Status = "failed"
				state = "failed"
			} else {
				now := time.Now()
				out.LastSentAt = &now
			}
			e.Deliveries[key] = out
			if progress, ok := s.repo.(accountOpsDeliveryRepository); ok {
				query, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				owned, saveErr := progress.SaveDelivery(query, e, key, out)
				cancel()
				if saveErr != nil || !owned {
					s.failures.Add(1)
					return
				}
			}
		}
	}
	s.finishNotification(ctx, e, state, delay)
}
func (s *AccountOpsService) finishNotification(ctx context.Context, e *AccountOpsEvent, state string, delay time.Duration) {
	if state == "failed" && e.Attempts < 3 {
		delay = 5 * time.Minute
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	if s.repo.Complete(finish, e, state, delay) != nil {
		s.failures.Add(1)
	}
}
