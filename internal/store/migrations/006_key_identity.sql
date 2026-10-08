CREATE FUNCTION authenticate_key_v2(key_digest text) RETURNS TABLE(key_id uuid,tenant_id uuid,name text,scopes text[],rate integer,daily_limit integer,paused boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=public,pg_temp AS $$
 SELECT k.id,t.id,t.name,k.scopes,t.rate,t.daily_limit,t.paused FROM public.api_keys k JOIN public.tenants t ON t.id=k.tenant_id WHERE k.digest=key_digest AND k.revoked_at IS NULL
$$;
CREATE FUNCTION authenticate_session(p_key uuid) RETURNS TABLE(key_id uuid,tenant_id uuid,name text,scopes text[],rate integer,daily_limit integer,paused boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=public,pg_temp AS $$
 SELECT k.id,t.id,t.name,k.scopes,t.rate,t.daily_limit,t.paused FROM public.api_keys k JOIN public.tenants t ON t.id=k.tenant_id WHERE k.id=p_key AND k.revoked_at IS NULL
$$;
CREATE FUNCTION authorize_key(p_key uuid) RETURNS TABLE(tenant_id uuid,scopes text[])
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path=public,pg_temp AS $$
 SELECT k.tenant_id,k.scopes FROM public.api_keys k WHERE k.id=p_key AND k.revoked_at IS NULL FOR SHARE OF k
$$;
REVOKE ALL ON FUNCTION authenticate_key_v2(text),authenticate_session(uuid),authorize_key(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION authenticate_key_v2(text),authenticate_session(uuid),authorize_key(uuid) TO mail_app,mail_worker;
