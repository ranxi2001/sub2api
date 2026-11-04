package balanceforecast

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const minimumRecoveryObservationGap = 10 * time.Minute

// RecoveryObservationGap allows a healthy user to survive one complete bounded
// sweep of eligible users plus one scheduling tick of slack. An exact multiple
// of candidatesPerSweep needs an additional tick to observe EOF and reset the
// persistent cursor, so the cycle is floor(count/budget)+1 rather than ceil.
func (s *Store) RecoveryObservationGap(ctx context.Context, candidatesPerSweep int, sweepInterval time.Duration) (time.Duration, error) {
	if candidatesPerSweep < 1 || sweepInterval <= 0 {
		return 0, errors.New("invalid forecast sweep limits")
	}
	countCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var eligible int64
	if err := s.DB.QueryRowContext(countCtx, `SELECT count(*) FROM users WHERE deleted_at IS NULL AND status='active' AND balance_notify_enabled AND balance>0`).Scan(&eligible); err != nil {
		return 0, err
	}
	ticks := eligible/int64(candidatesPerSweep) + 2
	if ticks > int64(time.Duration(1<<63-1)/sweepInterval) {
		return 0, errors.New("forecast recovery observation gap overflow")
	}
	gap := time.Duration(ticks) * sweepInterval
	if gap < minimumRecoveryObservationGap {
		gap = minimumRecoveryObservationGap
	}
	return gap, nil
}

// Claim reserves one whole notification round before any SMTP side effect.
// A sending row surviving a crash is deliberately never replayed: SMTP acceptance
// and a database commit cannot be made atomic. It continues to consume the cap.
func (s *Store) Claim(ctx context.Context, c Candidate, now time.Time, threshold float64, maxNotifications int) (int64, error) {
	return s.ClaimWithRecoveryGap(ctx, c, now, threshold, maxNotifications, minimumRecoveryObservationGap)
}

// ClaimWithRecoveryGap keeps recovery's continuity check aligned with the
// bounded scanner's actual revisit interval; it never removes the gap check.
func (s *Store) ClaimWithRecoveryGap(ctx context.Context, c Candidate, now time.Time, threshold float64, maxNotifications int, recoveryGap time.Duration) (int64, error) {
	if !c.Reliable() || threshold <= 0 || maxNotifications < 1 {
		return 0, nil
	}
	if recoveryGap < minimumRecoveryObservationGap {
		recoveryGap = minimumRecoveryObservationGap
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT INTO balance_forecast_episodes(user_id) VALUES($1) ON CONFLICT DO NOTHING`, c.ID); err != nil {
		return 0, err
	}
	var generation, sequence int64
	var active bool
	var healthy, lastHealthy, last sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT generation,sequence,active,healthy_since,healthy_last_seen,last_attempt_at FROM balance_forecast_episodes WHERE user_id=$1 FOR UPDATE`, c.ID).Scan(&generation, &sequence, &active, &healthy, &lastHealthy, &last)
	if err != nil {
		return 0, err
	}
	if c.Hours > threshold*1.2 {
		// A full hour above the recovery band closes the episode. Short jitter or
		// toggling the feature never clears the durable notification allowance.
		if !healthy.Valid || !lastHealthy.Valid || now.Sub(lastHealthy.Time) > recoveryGap {
			healthy = sql.NullTime{Time: now, Valid: true}
		}
		if now.Sub(healthy.Time) >= time.Hour {
			active = false
		}
		_, err = tx.ExecContext(ctx, `UPDATE balance_forecast_episodes SET healthy_since=$2,active=$3,healthy_last_seen=$4 WHERE user_id=$1`, c.ID, healthy.Time, active, now)
		if err != nil {
			return 0, err
		}
		return 0, tx.Commit()
	}
	if c.Hours > threshold {
		if _, err = tx.ExecContext(ctx, `UPDATE balance_forecast_episodes SET healthy_since=NULL,healthy_last_seen=NULL WHERE user_id=$1`, c.ID); err != nil {
			return 0, err
		}
		return 0, tx.Commit()
	}
	if !active {
		generation++
		sequence = 0
		last.Valid = false
	}
	_, err = tx.ExecContext(ctx, `UPDATE balance_forecast_episodes SET active=true,generation=$2,sequence=$3,healthy_since=NULL,healthy_last_seen=NULL WHERE user_id=$1`, c.ID, generation, sequence)
	if err != nil {
		return 0, err
	}
	if last.Valid && now.Sub(last.Time) < time.Hour {
		return 0, tx.Commit()
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM balance_forecast_outbox WHERE user_id=$1 AND generation=$2 AND state<>'skipped'`, c.ID, generation).Scan(&count); err != nil {
		return 0, err
	}
	if count >= maxNotifications {
		return 0, tx.Commit()
	}
	var id int64
	if err = tx.QueryRowContext(ctx, `INSERT INTO balance_forecast_outbox(user_id,generation,sequence,state,created_at) VALUES($1,$2,$3,'sending',$4) RETURNING id`, c.ID, generation, sequence+1, now).Scan(&id); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE balance_forecast_episodes SET sequence=$2,last_attempt_at=$3 WHERE user_id=$1`, c.ID, sequence+1, now); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// Finish never frees an uncertain send. Only suppression known to precede SMTP
// may release a reservation; a later worker cannot replay this outbox identity.
func (s *Store) Finish(ctx context.Context, id int64, state string, now time.Time) error {
	if state != "sent" && state != "uncertain" && state != "skipped" {
		return errors.New("invalid forecast delivery state")
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE balance_forecast_outbox SET state=$2,finished_at=$3 WHERE id=$1 AND state='sending'`, id, state, now)
	return err
}

// InterruptRecovery records a low-band observation even when all recipients
// have opted out, so preference changes cannot turn jitter into a new episode.
func (s *Store) InterruptRecovery(ctx context.Context, userID int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE balance_forecast_episodes SET healthy_since=NULL,healthy_last_seen=NULL WHERE user_id=$1 AND healthy_since IS NOT NULL`, userID)
	return err
}
