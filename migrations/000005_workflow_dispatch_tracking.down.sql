BEGIN;

DROP INDEX reconciliation_runs_unstarted_idx;

ALTER TABLE reconciliation_runs
    DROP COLUMN workflow_started_at;

COMMIT;
