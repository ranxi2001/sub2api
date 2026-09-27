-- Preserve the existing custom count before defaults can seed the new key.
-- Keep the legacy setting for application rollback.
INSERT INTO settings (key, value, updated_at)
SELECT 'excel_bps_image_max_images', value, NOW()
FROM settings
WHERE key = 'excel_bps_image_max_images_per_request'
ON CONFLICT (key) DO NOTHING;
