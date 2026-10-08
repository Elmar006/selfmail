-- Store MIME source and attachments once per batch, even for 100 recipients.
CREATE TABLE batch_payloads (
 batch_id uuid PRIMARY KEY REFERENCES batches(id), tenant_id uuid NOT NULL REFERENCES tenants(id),
 payload jsonb NOT NULL, raw_mime bytea
);
INSERT INTO batch_payloads(batch_id,tenant_id,payload,raw_mime)
 SELECT DISTINCT ON(batch_id) batch_id,tenant_id,payload,raw_mime FROM messages ORDER BY batch_id,id;
ALTER TABLE batch_payloads ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON batch_payloads
 USING (tenant_id=nullif(current_setting('selfmail.tenant',true),'')::uuid)
 WITH CHECK (tenant_id=nullif(current_setting('selfmail.tenant',true),'')::uuid);
GRANT SELECT,INSERT ON batch_payloads TO mail_app;
GRANT SELECT,INSERT,UPDATE,DELETE ON batch_payloads TO mail_worker;
ALTER TABLE messages DROP COLUMN payload, DROP COLUMN raw_mime;
