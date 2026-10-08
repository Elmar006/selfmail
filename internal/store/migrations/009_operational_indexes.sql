CREATE INDEX webhook_endpoint_pending ON webhook_jobs(endpoint_id,available_at,id) WHERE status='pending';
CREATE INDEX webhook_endpoint_dead ON webhook_jobs(endpoint_id,available_at,id) WHERE status='dead';
CREATE INDEX statistics_delta_age ON statistics_deltas(created_at,id);
CREATE INDEX messages_submitted_age ON messages(updated_at,id) WHERE status IN ('submitted','deferred');
CREATE INDEX mta_receipt_retention ON mta_receipts(updated_at);
CREATE INDEX audit_retention ON audit_log(created_at,id);
GRANT SELECT ON schema_migrations TO mail_app,mail_worker;
