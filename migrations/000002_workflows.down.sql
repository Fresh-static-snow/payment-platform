BEGIN;

DROP TABLE IF EXISTS reconciliation_issues;
DROP TABLE IF EXISTS reconciliation_runs;
DROP TABLE IF EXISTS refunds;

DELETE FROM ledger_entries
WHERE journal_id IN (
    SELECT id FROM ledger_journals WHERE reference_type = 'refund'
);
DELETE FROM ledger_journals WHERE reference_type = 'refund';

DROP INDEX IF EXISTS ledger_journals_payment_idx;
ALTER TABLE ledger_journals
    DROP CONSTRAINT IF EXISTS ledger_journals_reference_key,
    DROP CONSTRAINT IF EXISTS ledger_journals_reference_type_check,
    DROP COLUMN IF EXISTS reference_type,
    DROP COLUMN IF EXISTS reference_id,
    ADD CONSTRAINT ledger_journals_payment_id_key UNIQUE (payment_id);

COMMIT;
