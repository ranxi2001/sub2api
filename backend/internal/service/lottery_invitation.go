package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"slices"
	"strconv"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/lotterycontribution"
	"github.com/Wei-Shaw/sub2api/ent/lotteryinvitation"
	"github.com/Wei-Shaw/sub2api/ent/lotteryinvitelink"
	"github.com/Wei-Shaw/sub2api/ent/lotterypool"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type lotteryVisitorContextKey struct{}

func ContextWithLotteryVisitor(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, lotteryVisitorContextKey{}, token)
}

func lotteryVisitorKey(token string) string {
	if len(token) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(token); err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type LotteryInviteSummary struct {
	Path             string  `json:"path"`
	Visits           int     `json:"visits"`
	Registered       int     `json:"registered"`
	Qualified        int     `json:"qualified"`
	IncomingProgress float64 `json:"incoming_progress"`
	Invited          bool    `json:"invited"`
}

func (s *LotteryService) InviteLink(ctx context.Context, uid, pid int64) (*LotteryInviteSummary, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	c := tx.Client()
	if _, err = lotteryLockUser(ctx, c, uid); err != nil {
		return nil, err
	}
	enabled, err := lotteryEnabled(ctx, c, true)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, infraerrors.Forbidden("LOTTERY_DISABLED", "lottery disabled")
	}
	p, err := c.LotteryPool.Query().Where(lotterypool.IDEQ(pid), lotterypool.DeletedAtIsNil()).ForUpdate().Only(ctx)
	if err != nil {
		return nil, err
	}
	if !lotteryInviteMode(p.Mode) || p.ActivatedAt == nil {
		return nil, infraerrors.BadRequest("LOTTERY_INVALID_INVITE_RULE", "not an active invitation pool")
	}
	link, err := c.LotteryInviteLink.Query().Where(lotteryinvitelink.PoolIDEQ(pid), lotteryinvitelink.UserIDEQ(uid)).Only(ctx)
	if dbent.IsNotFound(err) {
		var bytes [32]byte
		if _, err = rand.Read(bytes[:]); err != nil {
			return nil, err
		}
		link, err = c.LotteryInviteLink.Create().SetPoolID(pid).SetUserID(uid).SetCode(hex.EncodeToString(bytes[:])).Save(ctx)
	}
	if err != nil {
		return nil, err
	}
	v := &LotteryInviteSummary{Path: "/register?lottery_invite=" + link.Code}
	q := c.LotteryInvitation.Query().Where(lotteryinvitation.PoolIDEQ(pid), lotteryinvitation.InviterIDEQ(uid))
	if v.Visits, err = q.Clone().Count(ctx); err != nil {
		return nil, err
	}
	if v.Registered, err = q.Clone().Where(lotteryinvitation.InviteeIDNotNil()).Count(ctx); err != nil {
		return nil, err
	}
	if v.Qualified, err = q.Clone().Where(lotteryinvitation.QualifiedEQ(true)).Count(ctx); err != nil {
		return nil, err
	}
	incoming, err := c.LotteryInvitation.Query().Where(lotteryinvitation.PoolIDEQ(pid), lotteryinvitation.InviteeIDEQ(uid)).Only(ctx)
	if err != nil && !dbent.IsNotFound(err) {
		return nil, err
	}
	if incoming != nil {
		v.Invited = true
		v.IncomingProgress = lotteryAmount(incoming.NetUnits)
	}
	return v, tx.Commit()
}

// RecordInviteVisit keeps the first inviter per browser/pool and checks IP and
// inviter caps while holding the pool lock, so concurrent visits cannot exceed them.
func (s *LotteryService) RecordInviteVisit(ctx context.Context, code, token, address, country string) (*LotteryPoolView, error) {
	key := lotteryVisitorKey(token)
	ip, parseErr := netip.ParseAddr(address)
	if len(code) != 64 || key == "" || parseErr != nil || ip.Zone() != "" {
		return nil, infraerrors.BadRequest("LOTTERY_INVALID_INVITE", "invalid invitation")
	}
	address = ip.Unmap().String()
	link, err := s.client.LotteryInviteLink.Query().Where(lotteryinvitelink.CodeEQ(code)).Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, infraerrors.NotFound("LOTTERY_INVALID_INVITE", "invitation unavailable")
	}
	if err != nil {
		return nil, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	c := tx.Client()
	u, err := lotteryLockUser(ctx, c, link.UserID)
	if err != nil {
		return nil, err
	}
	if u.Status != StatusActive {
		return nil, infraerrors.Forbidden("LOTTERY_INVALID_INVITE", "invitation unavailable")
	}
	enabled, err := lotteryEnabled(ctx, c, true)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, infraerrors.Forbidden("LOTTERY_DISABLED", "lottery disabled")
	}
	p, err := c.LotteryPool.Query().Where(lotterypool.IDEQ(link.PoolID), lotterypool.DeletedAtIsNil()).ForUpdate().Only(ctx)
	if err != nil {
		return nil, err
	}
	if !p.GrantEnabled || !lotteryInviteMode(p.Mode) {
		return nil, infraerrors.Conflict("LOTTERY_INVITE_STOPPED", "invitation rewards paused")
	}
	config := lotteryPoolInviteConfig(p)
	if !lotteryVisitAllowed(config, address, country) {
		return nil, infraerrors.Forbidden("LOTTERY_VISIT_BLOCKED", "this visit is not eligible for invitation rewards")
	}
	previous, err := c.LotteryInvitation.Query().Where(lotteryinvitation.PoolIDEQ(p.ID), lotteryinvitation.VisitorKeyEQ(key)).Only(ctx)
	if err != nil && !dbent.IsNotFound(err) {
		return nil, err
	}
	if previous == nil {
		day := time.Now().UTC().Truncate(24 * time.Hour)
		visits := c.LotteryInvitation.Query().Where(lotteryinvitation.PoolIDEQ(p.ID), lotteryinvitation.CreatedAtGTE(day))
		ipCount, err := visits.Clone().Where(lotteryinvitation.IPAddressEQ(address)).Count(ctx)
		if err != nil {
			return nil, err
		}
		inviterCount, err := visits.Clone().Where(lotteryinvitation.InviterIDEQ(link.UserID)).Count(ctx)
		if err != nil {
			return nil, err
		}
		if ipCount >= config.IPDailyLimit || inviterCount >= config.DailyLimit {
			return nil, infraerrors.Conflict("LOTTERY_VISIT_LIMIT", "daily invitation visit limit reached")
		}
		_, err = c.LotteryInvitation.Create().SetPoolID(p.ID).SetInviterID(link.UserID).SetVisitorKey(key).SetIPAddress(address).SetCountry(country).SetQualified(p.Mode == "invite_visit").Save(ctx)
		if err != nil {
			return nil, err
		}
		if _, err = c.LotteryPool.UpdateOneID(p.ID).SetRuleLocked(true).Save(ctx); err != nil {
			return nil, err
		}
	} else if previous.InviterID != link.UserID {
		return nil, infraerrors.Conflict("LOTTERY_INVITE_BOUND", "this browser already has an inviter for this pool")
	}
	if err = s.settleInvitationPoolTx(ctx, c, link.UserID, p, true); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	v := lotteryPoolView(p, nil, nil)
	lotteryPublicPool(&v)
	return &v, nil
}

// The receipt is bound only to an account created after the visit. It cannot be
// used to grant registration rewards to an existing or self-referring account.
func (s *LotteryService) RegisterInvitedUser(ctx context.Context, uid int64) error {
	if s == nil {
		return nil
	}
	token, _ := ctx.Value(lotteryVisitorContextKey{}).(string)
	key := lotteryVisitorKey(token)
	if key == "" {
		return nil
	}
	ids, err := s.client.LotteryInvitation.Query().Where(lotteryinvitation.VisitorKeyEQ(key), lotteryinvitation.CreatedAtGTE(time.Now().Add(-30*24*time.Hour)), lotteryinvitation.InviteeIDIsNil()).Order(dbent.Asc(lotteryinvitation.FieldPoolID)).All(ctx)
	if err != nil || len(ids) == 0 {
		return err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	c := tx.Client()
	u, err := lotteryLockUser(ctx, c, uid)
	if err != nil {
		return err
	}
	enabled, err := lotteryEnabled(ctx, c, true)
	if err != nil || !enabled {
		return err
	}
	for _, invitation := range ids {
		if uid == invitation.InviterID || u.CreatedAt.Before(invitation.CreatedAt) {
			continue
		}
		p, err := c.LotteryPool.Query().Where(lotterypool.IDEQ(invitation.PoolID)).ForUpdate().Only(ctx)
		if err != nil {
			return err
		}
		if p.DeletedAt != nil || !p.GrantEnabled {
			continue
		}
		exists, err := c.LotteryInvitation.Query().Where(lotteryinvitation.PoolIDEQ(p.ID), lotteryinvitation.InviteeIDEQ(uid)).Exist(ctx)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		_, err = c.LotteryInvitation.Update().Where(lotteryinvitation.IDEQ(invitation.ID), lotteryinvitation.InviteeIDIsNil()).SetInviteeID(uid).SetRegisteredAt(u.CreatedAt).SetQualified(p.Mode != "invite_recharge").Save(ctx)
		if err != nil {
			return err
		}
	}
	s.invitationAfterCommit(tx, uid)
	return tx.Commit()
}

func (s *LotteryService) recordInvitationRechargeTx(ctx context.Context, c *dbent.Client, p *dbent.LotteryPool, uid int64, source, sourceID string, units int64) error {
	i, err := c.LotteryInvitation.Query().Where(lotteryinvitation.PoolIDEQ(p.ID), lotteryinvitation.InviteeIDEQ(uid)).Only(ctx)
	if dbent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	exists, err := c.LotteryContribution.Query().Where(lotterycontribution.PoolIDEQ(p.ID), lotterycontribution.SourceTypeEQ(source), lotterycontribution.SourceIDEQ(sourceID)).Exist(ctx)
	if err != nil || exists {
		return err
	}
	if units > math.MaxInt64-i.NetUnits {
		return fmt.Errorf("invitation amount overflow")
	}
	if _, err = c.LotteryContribution.Create().SetPoolID(p.ID).SetUserID(uid).SetSourceType(source).SetSourceID(sourceID).SetAmountUnits(units).SetNetUnits(units).Save(ctx); err != nil {
		return err
	}
	_, err = c.LotteryInvitation.UpdateOneID(i.ID).AddNetUnits(units).SetQualified(i.NetUnits+units >= p.ThresholdUnits).Save(ctx)
	return err
}

func (s *LotteryService) refundInvitationTx(ctx context.Context, c *dbent.Client, p *dbent.LotteryPool, contribution *dbent.LotteryContribution, net int64) error {
	i, err := c.LotteryInvitation.Query().Where(lotteryinvitation.PoolIDEQ(p.ID), lotteryinvitation.InviteeIDEQ(contribution.UserID)).Only(ctx)
	if err != nil {
		return err
	}
	total := i.NetUnits - (contribution.NetUnits - net)
	if total < 0 {
		return fmt.Errorf("invalid invitation refund accounting")
	}
	if _, err = c.LotteryContribution.UpdateOneID(contribution.ID).SetNetUnits(net).Save(ctx); err != nil {
		return err
	}
	_, err = c.LotteryInvitation.UpdateOneID(i.ID).SetNetUnits(total).SetQualified(total >= p.ThresholdUnits).Save(ctx)
	return err
}

// Qualification is durable in the source transaction. Recipient settlement runs
// after commit using user -> pools locks; draw also settles under its pool lock,
// preventing a concurrent refund from spending stale invitation eligibility.
func (s *LotteryService) invitationAfterCommit(tx *dbent.Tx, invitee int64) {
	tx.OnCommit(func(next dbent.Committer) dbent.Committer {
		return dbent.CommitFunc(func(ctx context.Context, tx *dbent.Tx) error {
			if err := next.Commit(ctx, tx); err != nil {
				return err
			}
			settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := s.syncInvitationParticipants(settleCtx, invitee); err != nil {
				slog.Warn("lottery invitation settlement pending", "user_id", invitee, "error", err)
			}
			return nil
		})
	})
}

func (s *LotteryService) syncInvitationParticipants(ctx context.Context, uid int64) error {
	rows, err := s.client.LotteryInvitation.Query().Where(lotteryinvitation.InviteeIDEQ(uid)).All(ctx)
	if err != nil || len(rows) == 0 {
		return err
	}
	users := []int64{uid}
	for _, i := range rows {
		users = append(users, i.InviterID)
	}
	slices.Sort(users)
	for _, id := range slices.Compact(users) {
		if err := s.SyncInvitationRewards(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *LotteryService) SyncInvitationRewards(ctx context.Context, uid int64) error {
	rows, err := s.client.LotteryInvitation.Query().Where(lotteryinvitation.Or(lotteryinvitation.InviterIDEQ(uid), lotteryinvitation.InviteeIDEQ(uid))).All(ctx)
	if err != nil || len(rows) == 0 {
		return err
	}
	pids := make([]int64, 0, len(rows))
	for _, i := range rows {
		pids = append(pids, i.PoolID)
	}
	slices.Sort(pids)
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	c := tx.Client()
	if _, err = lotteryLockUser(ctx, c, uid); err != nil {
		return err
	}
	enabled, err := lotteryEnabled(ctx, c, true)
	if err != nil {
		return err
	}
	for _, pid := range slices.Compact(pids) {
		p, err := c.LotteryPool.Query().Where(lotterypool.IDEQ(pid)).ForUpdate().Only(ctx)
		if err != nil {
			return err
		}
		if err = s.settleInvitationPoolTx(ctx, c, uid, p, enabled); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *LotteryService) settleInvitationPoolTx(ctx context.Context, c *dbent.Client, uid int64, p *dbent.LotteryPool, enabled bool) error {
	if !lotteryInviteMode(p.Mode) {
		return nil
	}
	rows, err := c.LotteryInvitation.Query().Where(lotteryinvitation.PoolIDEQ(p.ID), lotteryinvitation.Or(lotteryinvitation.InviterIDEQ(uid), lotteryinvitation.InviteeIDEQ(uid))).All(ctx)
	if err != nil {
		return err
	}
	config := lotteryPoolInviteConfig(p)
	for _, i := range rows {
		inviter := uid == i.InviterID
		old, desired := i.InviterAwarded, i.Qualified
		if !inviter {
			old = i.InviteeAwarded
			desired = desired && config.RewardTo == "both" && i.InviteeID != nil
		}
		if old == desired || (!old && (!enabled || p.DeletedAt != nil)) {
			continue
		}
		delta, action := config.Tickets, "invite"
		if !desired {
			delta = -delta
			action = "refund"
		}
		a, err := lotteryAccountForUpdate(ctx, c, p.ID, uid)
		if err != nil {
			return err
		}
		if (delta < 0 && a.Earned < -delta) || (delta > 0 && a.Earned > math.MaxInt64-delta) {
			return fmt.Errorf("invalid invitation ticket balance")
		}
		if _, err = c.LotteryAccount.UpdateOneID(a.ID).AddEarned(delta).Save(ctx); err != nil {
			return err
		}
		update := c.LotteryInvitation.UpdateOneID(i.ID)
		if inviter {
			update.SetInviterAwarded(desired)
		} else {
			update.SetInviteeAwarded(desired)
		}
		if _, err = update.Save(ctx); err != nil {
			return err
		}
		if _, err = c.LotteryLedger.Create().SetPoolID(p.ID).SetUserID(uid).SetAction(action).SetSourceType(p.Mode).SetSourceID(strconv.FormatInt(i.ID, 10)).SetAmountUnits(0).SetTicketDelta(delta).Save(ctx); err != nil {
			return err
		}
	}
	return nil
}

type LotteryInvitationView struct {
	ID             int64     `json:"id"`
	PoolID         int64     `json:"pool_id"`
	InviterID      int64     `json:"inviter_id"`
	InviteeID      *int64    `json:"invitee_id"`
	IPAddress      string    `json:"ip_address"`
	Country        string    `json:"country"`
	Amount         float64   `json:"amount"`
	Qualified      bool      `json:"qualified"`
	InviterAwarded bool      `json:"inviter_awarded"`
	InviteeAwarded bool      `json:"invitee_awarded"`
	CreatedAt      time.Time `json:"created_at"`
}

func (s *LotteryService) Invitations(ctx context.Context, uid, pid int64, page, size int) ([]LotteryInvitationView, int, error) {
	q := s.client.LotteryInvitation.Query()
	if uid > 0 {
		q.Where(lotteryinvitation.Or(lotteryinvitation.InviterIDEQ(uid), lotteryinvitation.InviteeIDEQ(uid)))
	}
	if pid > 0 {
		q.Where(lotteryinvitation.PoolIDEQ(pid))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Order(dbent.Desc(lotteryinvitation.FieldID)).Offset((page - 1) * size).Limit(size).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	views := make([]LotteryInvitationView, 0, len(rows))
	for _, i := range rows {
		views = append(views, LotteryInvitationView{ID: i.ID, PoolID: i.PoolID, InviterID: i.InviterID, InviteeID: i.InviteeID, IPAddress: i.IPAddress, Country: i.Country, Amount: lotteryAmount(i.NetUnits), Qualified: i.Qualified, InviterAwarded: i.InviterAwarded, InviteeAwarded: i.InviteeAwarded, CreatedAt: i.CreatedAt})
	}
	return views, total, nil
}
