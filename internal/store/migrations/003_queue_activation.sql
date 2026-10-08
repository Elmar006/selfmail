-- cleanup can log Message-ID before the queue commit; qmgr activation or a
-- destination delivery result is stronger evidence of durable MTA acceptance.
ALTER TABLE mta_receipts ADD COLUMN activated boolean NOT NULL DEFAULT false;
