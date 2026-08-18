-- Add container_id to deployments to track the runtime container per deployment.
ALTER TABLE deployments
    ADD COLUMN container_id TEXT;
