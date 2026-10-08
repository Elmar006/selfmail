CREATE INDEX messages_batch ON messages(batch_id);
CREATE INDEX batches_retention ON batches(tenant_id,created_at,id);
CREATE INDEX batches_response_messages ON batches USING gin((response->'message_ids'));
CREATE INDEX webhook_jobs_retention ON webhook_jobs(available_at) WHERE status IN ('delivered','dead');
