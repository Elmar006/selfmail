-- Recovery stores an audit-only surrogate for keys whose plaintext is absent
-- from the independent journal. Only the normalized key digest defines identity.
ALTER TABLE batches DROP CONSTRAINT IF EXISTS batches_tenant_id_idempotency_key_key;
