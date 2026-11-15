package qqbot

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/lib/pq"
)

func (s *Service) startGrowthMonitor(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		var after int64
		var appID string
		scan := func() {
			q, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			c, err := s.Config(q)
			if err == nil {
				if appID != c.AppID {
					after, appID = 0, c.AppID
				}
				after, err = s.settleGrowthBatch(q, c, after)
			}
			if err != nil && ctx.Err() == nil {
				slog.Warn("qqbot growth settlement failed", "error", err)
			}
		}
		scan()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				scan()
			}
		}
	}()
}

// A rotating keyset cursor bounds each pass and prevents incomplete or failing
// early accounts from starving later accounts. Recheck eligibility in the grant
// transaction; this candidate query cannot authorize a reward by itself.
func (s *Service) settleGrowthBatch(ctx context.Context, c Config, after int64) (int64, error) {
	if !c.Enabled || c.AppID == "" {
		return 0, nil
	}
	cfg, err := loadCheckinConfig(ctx, s.db, c.CheckinReward)
	if err != nil {
		return after, err
	}
	var ids []string
	at := time.Now()
	for _, task := range cfg.Growth.tasks() {
		if task.Enabled && task.reward().enabled() {
			ids = append(ids, growthTaskKeySuffix(task, at))
			if period := growthTaskPeriod(task); period != "once" {
				ids = append(ids, growthTaskKeySuffix(task, growthPreviousPeriodEnd(period, at)))
			}
		}
	}
	periodic := false
	for _, milestone := range cfg.Milestones {
		periodic = periodic || (cfg.Enabled && milestone.Enabled && milestone.reward().enabled())
	}
	for _, task := range cfg.Growth.PeriodicTasks {
		periodic = periodic || (task.Enabled && task.reward().enabled())
	}
	if len(ids) == 0 && !periodic {
		return 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT b.user_id,b.member_id,b.group_id FROM qq_bot_bindings b
 JOIN users u ON u.id=b.user_id AND u.status='active' AND u.deleted_at IS NULL
 WHERE b.app_id=$1 AND b.user_id>$2 AND b.group_id=ANY($3)
 AND ($5 OR EXISTS(SELECT 1 FROM unnest($4::text[]) task_id
 WHERE NOT EXISTS(SELECT 1 FROM qq_bot_reward_codes r WHERE r.source_key='qq-growth:'||b.user_id::text||':'||task_id)
 AND NOT EXISTS(SELECT 1 FROM qq_bot_lottery_chance_grants g WHERE g.source_key='qq-growth:'||b.user_id::text||':'||task_id)))
 ORDER BY b.user_id LIMIT 100`, c.AppID, after, pq.Array(c.AllowedGroups), pq.Array(ids), periodic)
	if err != nil {
		return after, err
	}
	type candidate struct {
		uid int64
		job Job
	}
	var candidates []candidate
	for rows.Next() {
		var candidate candidate
		if err = rows.Scan(&candidate.uid, &candidate.job.MemberID, &candidate.job.GroupID); err != nil {
			break
		}
		candidates = append(candidates, candidate)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return after, err
	}
	var failures error
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return after, errors.Join(failures, ctx.Err())
		}
		_, _, err = s.processGrowthTasks(ctx, c, candidate.job, time.Now(), false)
		failures = errors.Join(failures, err)
		after = candidate.uid
	}
	if len(candidates) < 100 {
		after = 0
	}
	return after, failures
}
