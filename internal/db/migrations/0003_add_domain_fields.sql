-- Track DNS verification completion per domain and the application's current
-- generated URL so routing state survives control-plane restarts.
ALTER TABLE domains ADD COLUMN verified_at TEXT;

ALTER TABLE applications ADD COLUMN current_deployed_url TEXT;
