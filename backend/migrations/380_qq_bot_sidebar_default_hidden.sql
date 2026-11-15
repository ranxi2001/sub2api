-- QQ was not previously selectable, so existing preferences cannot express a
-- choice to show it. Hide it once on upgrade; later saves can show it normally.
DO $$
DECLARE
    hidden_items jsonb;
BEGIN
    BEGIN
        SELECT value::jsonb INTO hidden_items
        FROM settings WHERE key = 'smart_ops_hidden_menu_items';
    EXCEPTION WHEN invalid_text_representation THEN
        -- Invalid and missing preferences use the application's defaults.
        RETURN;
    END;

    IF jsonb_typeof(hidden_items) IS DISTINCT FROM 'array' THEN
        RETURN;
    END IF;

    IF NOT hidden_items ? '/admin/qqbot/overview' THEN
        UPDATE settings
        SET value = (hidden_items || '["/admin/qqbot/overview"]'::jsonb)::text,
            updated_at = NOW()
        WHERE key = 'smart_ops_hidden_menu_items';
    END IF;
END $$;
