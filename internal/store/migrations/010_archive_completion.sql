ALTER TABLE mta_log_cursors ADD COLUMN completed_at timestamptz;
CREATE INDEX mta_cursor_completed ON mta_log_cursors(completed_at) WHERE completed_at IS NOT NULL;
