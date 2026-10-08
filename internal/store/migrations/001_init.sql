CREATE TABLE tenants (
 id uuid PRIMARY KEY, name text NOT NULL, rate integer NOT NULL DEFAULT 10 CHECK(rate BETWEEN 1 AND 10000),
 daily_limit integer NOT NULL DEFAULT 10000 CHECK(daily_limit>0), paused boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE api_keys (
 id uuid PRIMARY KEY, tenant_id uuid NOT NULL REFERENCES tenants(id), digest text UNIQUE NOT NULL,
 scopes text[] NOT NULL, revoked_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE sender_domains (
 id uuid PRIMARY KEY, tenant_id uuid NOT NULL REFERENCES tenants(id), name text NOT NULL UNIQUE,
 selector text NOT NULL, verification_token text NOT NULL, public_key text NOT NULL, encrypted_key bytea NOT NULL,
 verified_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(tenant_id,name)
);
CREATE TABLE templates (
 tenant_id uuid NOT NULL REFERENCES tenants(id), name text NOT NULL, version integer NOT NULL,
 subject text NOT NULL, body_text text NOT NULL, body_html text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,name,version)
);
CREATE TABLE batches (
 id uuid PRIMARY KEY, tenant_id uuid NOT NULL REFERENCES tenants(id), idempotency_key text NOT NULL,
 fingerprint text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(tenant_id,idempotency_key)
);
CREATE TABLE messages (
 id uuid PRIMARY KEY, tenant_id uuid NOT NULL REFERENCES tenants(id), batch_id uuid NOT NULL REFERENCES batches(id),
 sender text NOT NULL, recipient text NOT NULL, priority text NOT NULL CHECK(priority IN ('critical','normal','bulk')),
 payload jsonb NOT NULL, raw_mime bytea,
 status text NOT NULL CHECK(status IN ('queued','dispatching','submission_unknown','submitted','deferred','delivered','bounced','failed','canceled','suppressed')),
 attempt_count integer NOT NULL DEFAULT 0, attempt_id uuid, queue_id text NOT NULL DEFAULT '',
 lease_until timestamptz, next_attempt_at timestamptz NOT NULL, expires_at timestamptz NOT NULL,
 last_error text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX messages_tenant_cursor ON messages(tenant_id,created_at DESC,id DESC);
CREATE INDEX messages_recovery ON messages(lease_until) WHERE status='dispatching';
CREATE TABLE attempts (
 id uuid PRIMARY KEY, tenant_id uuid NOT NULL REFERENCES tenants(id), message_id uuid NOT NULL REFERENCES messages(id),
 phase text NOT NULL CHECK(phase IN ('preparing','sending','accepted','rejected','unknown')), node_id text NOT NULL,
 queue_id text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX attempts_message ON attempts(message_id);
CREATE TABLE events (
 id uuid PRIMARY KEY, tenant_id uuid NOT NULL REFERENCES tenants(id), message_id uuid NOT NULL REFERENCES messages(id),
 type text NOT NULL, details jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX events_message ON events(tenant_id,message_id,created_at,id);
CREATE TABLE suppressions (
 tenant_id uuid NOT NULL REFERENCES tenants(id), recipient text NOT NULL, reason text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,recipient)
);
CREATE TABLE daily_usage (
 tenant_id uuid NOT NULL REFERENCES tenants(id), day date NOT NULL, count integer NOT NULL CHECK(count>=0), PRIMARY KEY(tenant_id,day)
);
CREATE TABLE outbox (
 id bigserial PRIMARY KEY, tenant_id uuid NOT NULL REFERENCES tenants(id), message_id uuid NOT NULL REFERENCES messages(id),
 priority text NOT NULL, available_at timestamptz NOT NULL DEFAULT now(), published_at timestamptz
);
CREATE INDEX outbox_pending ON outbox(available_at,id) WHERE published_at IS NULL;
CREATE TABLE webhook_endpoints (
 id uuid PRIMARY KEY, tenant_id uuid NOT NULL REFERENCES tenants(id), url text NOT NULL, encrypted_secret bytea NOT NULL,
 enabled boolean NOT NULL DEFAULT true, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE webhook_jobs (
 id uuid PRIMARY KEY, tenant_id uuid NOT NULL REFERENCES tenants(id), endpoint_id uuid NOT NULL REFERENCES webhook_endpoints(id), event_id uuid NOT NULL REFERENCES events(id),
 payload jsonb NOT NULL, status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','delivered','dead')),
 attempt_count integer NOT NULL DEFAULT 0, available_at timestamptz NOT NULL DEFAULT now(), last_error text NOT NULL DEFAULT '', UNIQUE(endpoint_id,event_id)
);
CREATE INDEX webhook_pending ON webhook_jobs(available_at) WHERE status='pending';
CREATE TABLE mta_log_cursors (node_id text NOT NULL, file_id text NOT NULL, offset_bytes bigint NOT NULL DEFAULT 0, updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(node_id,file_id));
CREATE TABLE mta_receipts (
 node_id text NOT NULL, queue_id text NOT NULL, attempt_id uuid, message_id uuid, status text NOT NULL DEFAULT '',
 recipient text NOT NULL DEFAULT '', dsn text NOT NULL DEFAULT '', diagnostic text NOT NULL DEFAULT '', updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(node_id,queue_id)
);
CREATE TABLE mta_log_lines (node_id text NOT NULL, file_id text NOT NULL, offset_bytes bigint NOT NULL, PRIMARY KEY(node_id,file_id,offset_bytes));
CREATE TABLE audit_log (id bigserial PRIMARY KEY, tenant_id uuid REFERENCES tenants(id), action text NOT NULL, details jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now());

DO $$ DECLARE t text; BEGIN
 FOREACH t IN ARRAY ARRAY['tenants','sender_domains','templates','batches','messages','attempts','events','suppressions','daily_usage','outbox','webhook_endpoints','webhook_jobs','audit_log'] LOOP
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',t);
  IF t='tenants' THEN
   EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (id = nullif(current_setting(''selfmail.tenant'',true),'''')::uuid) WITH CHECK (id = nullif(current_setting(''selfmail.tenant'',true),'''')::uuid)',t);
  ELSE
   EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = nullif(current_setting(''selfmail.tenant'',true),'''')::uuid) WITH CHECK (tenant_id = nullif(current_setting(''selfmail.tenant'',true),'''')::uuid)',t);
  END IF;
 END LOOP;
END $$;

CREATE FUNCTION authenticate_key(key_digest text) RETURNS TABLE(tenant_id uuid,name text,scopes text[],rate integer,daily_limit integer,paused boolean)
 LANGUAGE sql STABLE SECURITY DEFINER SET search_path=public,pg_temp AS $$
 SELECT t.id,t.name,k.scopes,t.rate,t.daily_limit,t.paused FROM public.api_keys k JOIN public.tenants t ON t.id=k.tenant_id WHERE k.digest=key_digest AND k.revoked_at IS NULL
$$;
REVOKE ALL ON FUNCTION authenticate_key(text) FROM PUBLIC;

GRANT USAGE ON SCHEMA public TO mail_app,mail_worker;
GRANT SELECT,INSERT,UPDATE,DELETE ON tenants,sender_domains,templates,batches,messages,events,suppressions,daily_usage,outbox,webhook_endpoints,webhook_jobs,audit_log TO mail_app;
GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO mail_app,mail_worker;
GRANT EXECUTE ON FUNCTION authenticate_key(text) TO mail_app,mail_worker;
GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO mail_worker;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
