-- Extend existing one-shot rows without changing their behavior. Forward only.
ALTER TABLE account_schedulable_timers
    ADD COLUMN IF NOT EXISTS recurrence TEXT NOT NULL DEFAULT 'once'
        CHECK (recurrence IN ('once', 'daily')),
    ADD COLUMN IF NOT EXISTS daily_time TEXT NOT NULL DEFAULT '';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'account_schedulable_timers'::regclass
          AND conname = 'account_schedulable_timers_daily_time_check'
    ) THEN
        ALTER TABLE account_schedulable_timers
            ADD CONSTRAINT account_schedulable_timers_daily_time_check CHECK (
                (recurrence = 'once' AND daily_time = '') OR
                (recurrence = 'daily' AND daily_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$')
            );
    END IF;
END $$;

ALTER TABLE account_schedulable_timer_audit
    ADD COLUMN IF NOT EXISTS recurrence TEXT NOT NULL DEFAULT 'once',
    ADD COLUMN IF NOT EXISTS daily_time TEXT NOT NULL DEFAULT '';
