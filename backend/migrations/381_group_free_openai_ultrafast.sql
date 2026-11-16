ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS free_openai_ultrafast BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN groups.free_openai_ultrafast IS
    'Whether UltraFast requests in this OpenAI/Composite group are billed to users at Standard price';
