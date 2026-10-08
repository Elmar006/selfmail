UPDATE mta_receipts SET activated=true WHERE status IN ('sent','deferred','bounced');
