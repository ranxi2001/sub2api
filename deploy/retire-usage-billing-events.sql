-- Run with psql after all users of this database have upgraded, in a maintenance window.
-- An old binary must never resume staging after the journal is dropped.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '3s';
DO $retire$
BEGIN
    IF to_regclass('public.usage_billing_events') IS NULL THEN
        RETURN;
    END IF;
    LOCK TABLE public.usage_billing_events IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT FROM public.usage_billing_events WHERE state NOT IN ('complete', 'legacy')) THEN
        RAISE EXCEPTION 'Unfinished billing events remain. Drain pending/applied and reconcile blocked events before retiring history.';
    END IF;
    DROP TABLE public.usage_billing_events;
    DROP FUNCTION IF EXISTS public.reject_usage_billing_event_identity_mutation();
END
$retire$;
COMMIT;
