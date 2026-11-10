-- Replace the legacy, invisible degradation model gate with ordinary account
-- configuration. Concurrency and group removals were already persisted by the
-- old evaluator. Keep its marker only as audit/idempotency metadata.
-- This is a one-time conversion: rerunning it must not undo later admin edits.
DO $$
DECLARE
    account_row RECORD;
    action JSONB;
    scope JSONB;
    raw_mapping JSONB;
    native_mapping JSONB;
    next_credentials JSONB;
    next_extra JSONB;
    next_schedulable BOOLEAN;
    source_model TEXT;
    target_model TEXT;
    matched BOOLEAN;
    allows_identity BOOLEAN;
    invalid_action BOOLEAN;
    native_scheduling_paused BOOLEAN;
BEGIN
    FOR account_row IN
        SELECT id, platform, credentials, extra, schedulable, concurrency
        FROM accounts
        WHERE type = 'oauth' AND deleted_at IS NULL
          AND COALESCE(extra, '{}'::jsonb) ? 'oauth_degradation_action'
          AND extra->'oauth_degradation_action'->'native_controls' IS DISTINCT FROM 'true'::jsonb
        FOR UPDATE
    LOOP
        action := account_row.extra->'oauth_degradation_action';
        -- Invalid legacy state previously denied models. Represent that safely
        -- with the visible scheduling switch, rather than silently allowing all.
        IF jsonb_typeof(action) IS DISTINCT FROM 'object' THEN
            action := jsonb_build_object('legacy_state', action, 'limit_models', true);
        END IF;
        next_credentials := account_row.credentials;
        next_extra := account_row.extra;
        next_schedulable := account_row.schedulable;
        native_scheduling_paused := false;
        invalid_action :=
            (action ? 'limit_models' AND jsonb_typeof(action->'limit_models') NOT IN ('boolean', 'null'))
            OR (action ? 'limit_concurrency' AND jsonb_typeof(action->'limit_concurrency') NOT IN ('boolean', 'null'))
            OR (action ? 'limit_groups' AND jsonb_typeof(action->'limit_groups') NOT IN ('boolean', 'null'))
            OR (action ? 'models' AND jsonb_typeof(action->'models') NOT IN ('array', 'null'));
        IF jsonb_typeof(action->'models') = 'array' THEN
            invalid_action := invalid_action OR EXISTS (
                SELECT 1 FROM jsonb_array_elements(action->'models') v WHERE jsonb_typeof(v) <> 'string');
        END IF;
        IF invalid_action THEN
            next_schedulable := false;
            native_scheduling_paused := true;
        END IF;

        IF action->'limit_models' IS DISTINCT FROM 'false'::jsonb THEN
            scope := CASE WHEN jsonb_typeof(action->'models') = 'array'
                THEN action->'models' ELSE '[]'::jsonb END;
            IF invalid_action THEN
                scope := '[]'::jsonb;
            END IF;
            raw_mapping := CASE WHEN jsonb_typeof(account_row.credentials->'model_mapping') = 'object'
                THEN account_row.credentials->'model_mapping' ELSE '{}'::jsonb END;
            native_mapping := '{}'::jsonb;
            -- Default/implicit mappings on other platforms cannot be resolved
            -- faithfully in SQL. Only carry over persisted aliases there.
            allows_identity :=
                (raw_mapping = '{}'::jsonb AND account_row.platform IN ('openai', 'anthropic'))
                OR (account_row.platform = 'openai' AND (
                    account_row.credentials->>'model_mapping_mode' = 'aliases'
                    OR account_row.extra->'openai_passthrough' = 'true'::jsonb
                    OR (NOT (account_row.extra ? 'openai_passthrough')
                        AND account_row.extra->'openai_oauth_passthrough' = 'true'::jsonb)));

            FOR source_model IN
                SELECT value #>> '{}' FROM jsonb_array_elements(scope)
                WHERE jsonb_typeof(value) = 'string'
                  AND value #>> '{}' <> '' AND position('*' IN (value #>> '{}')) = 0
            LOOP
                -- Same exact-first, longest trailing-wildcard precedence as
                -- resolveRequestedModelInMapping. '%' and '_' stay literal.
                SELECT value #>> '{}' INTO target_model
                FROM jsonb_each(raw_mapping)
                WHERE jsonb_typeof(value) = 'string'
                  AND (key = source_model OR (right(key, 1) = '*'
                    AND starts_with(source_model, left(key, length(key) - 1))))
                ORDER BY (key = source_model) DESC, length(key) DESC, key COLLATE "C"
                LIMIT 1;
                matched := FOUND;
                IF NOT matched AND COALESCE(allows_identity, false) THEN
                    target_model := source_model;
                    matched := true;
                END IF;
                -- The old gate checked both the public name and mapped target.
                IF matched AND target_model <> '' AND position('*' IN target_model) = 0
                    AND scope ? target_model THEN
                    native_mapping := native_mapping || jsonb_build_object(source_model, target_model);
                END IF;
            END LOOP;

            IF native_mapping = '{}'::jsonb OR jsonb_typeof(next_credentials) IS DISTINCT FROM 'object'
                OR (account_row.credentials ? 'model_mapping'
                    AND jsonb_typeof(account_row.credentials->'model_mapping') NOT IN ('object', 'null')) THEN
                -- Empty ordinary mappings mean default support; use the native
                -- scheduling switch instead, keeping the previous mapping editable.
                next_schedulable := false;
                native_scheduling_paused := true;
            ELSE
                next_credentials := (next_credentials - 'compact_model_mapping')
                    || jsonb_build_object('model_mapping', native_mapping, 'model_mapping_mode', 'whitelist');
                next_extra := next_extra || '{"openai_passthrough":false,"openai_oauth_passthrough":false}'::jsonb;
            END IF;
        END IF;

        next_extra := jsonb_set(next_extra, '{oauth_degradation_action}',
            action || jsonb_build_object('native_controls', true,
                'native_scheduling_paused', native_scheduling_paused));
        IF action->'limit_concurrency' IS DISTINCT FROM 'false'::jsonb THEN
            next_extra := jsonb_set(next_extra, '{auto_config_concurrency}',
                jsonb_build_object('concurrency', account_row.concurrency, 'degradation_hold', true));
        END IF;
        UPDATE accounts SET credentials = next_credentials, extra = next_extra,
            schedulable = next_schedulable,
            updated_at = GREATEST(clock_timestamp(), updated_at + INTERVAL '1 microsecond')
        WHERE id = account_row.id;
        INSERT INTO scheduler_outbox(event_type, account_id) VALUES ('account_changed', account_row.id);
    END LOOP;
END $$;
