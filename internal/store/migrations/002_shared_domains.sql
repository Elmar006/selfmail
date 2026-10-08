-- Several projects owned by the same DNS operator can share a sender domain.
-- Each project must prove control and publish its own unique DKIM selector.
ALTER TABLE sender_domains DROP CONSTRAINT IF EXISTS sender_domains_name_key;
