BEGIN;

-- Kept separate from the workflow hardening migration because existing
-- installations may already have applied version 3. The timestamp is the
-- durable hand-off marker used by the recovery dispatcher after a crash
-- between committing a run and starting its Temporal workflow.
ALTER TABLE reconciliation_runs
    ADD COLUMN workflow_started_at TIMESTAMPTZ;

CREATE INDEX reconciliation_runs_unstarted_idx
    ON reconciliation_runs (created_at, id)
    WHERE workflow_started_at IS NULL AND status = 'pending';

COMMIT;
