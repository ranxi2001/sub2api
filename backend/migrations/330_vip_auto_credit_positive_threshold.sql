-- Forward correction: 328 has already been exercised and is immutable.
-- A zero threshold was accepted by the first Draft increment. Disable it and
-- clear that invalid setting while retaining all ledger/audit history.
UPDATE auto_credit_config SET enabled=false,threshold=NULL,version=version+1
WHERE threshold=0;
ALTER TABLE auto_credit_config ADD CONSTRAINT auto_credit_threshold_positive
CHECK(threshold IS NULL OR threshold>0);
