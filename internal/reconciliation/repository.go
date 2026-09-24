package reconciliation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrRunNotFound = errors.New("reconciliation run not found")
	ErrInvalidRun  = errors.New("invalid reconciliation run")
)

type Run struct {
	ID                uuid.UUID      `json:"id"`
	WorkflowID        string         `json:"workflow_id"`
	RequestedBy       string         `json:"requested_by"`
	Status            string         `json:"status"`
	IssueCount        int            `json:"issue_count"`
	Summary           map[string]int `json:"summary"`
	FailureReason     string         `json:"failure_reason,omitempty"`
	WorkflowStartedAt *time.Time     `json:"workflow_started_at,omitempty"`
	StartedAt         *time.Time     `json:"started_at,omitempty"`
	FinishedAt        *time.Time     `json:"finished_at,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
}

const runColumns = `id,workflow_id,requested_by,status,issue_count,summary,failure_reason,
workflow_started_at,started_at,finished_at,created_at`

type Issue struct {
	ID          uuid.UUID       `json:"id"`
	RunID       uuid.UUID       `json:"run_id"`
	Type        string          `json:"issue_type"`
	ReferenceID uuid.UUID       `json:"reference_id"`
	Details     json.RawMessage `json:"details"`
	CreatedAt   time.Time       `json:"created_at"`
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) CreateRun(ctx context.Context, requestedBy string) (Run, error) {
	requestedBy = strings.TrimSpace(requestedBy)
	if requestedBy == "" {
		return Run{}, fmt.Errorf("%w: requested_by is required", ErrInvalidRun)
	}
	return scanRun(r.pool.QueryRow(ctx, `
		WITH new_uuid AS MATERIALIZED (SELECT uuidv7() AS id)
		INSERT INTO reconciliation_runs(id,workflow_id,requested_by,status)
		SELECT id,'reconciliation:' || id::text,$1,'pending'
		FROM new_uuid
		RETURNING `+runColumns+`
	`, requestedBy))
}

func (r *Repository) GetRun(ctx context.Context, id uuid.UUID) (Run, error) {
	return scanRun(r.pool.QueryRow(ctx, `
		SELECT `+runColumns+`
		FROM reconciliation_runs WHERE id=$1
	`, id))
}

func scanRun(row pgx.Row) (Run, error) {
	var result Run
	var summary []byte
	err := row.Scan(
		&result.ID, &result.WorkflowID, &result.RequestedBy, &result.Status,
		&result.IssueCount, &summary, &result.FailureReason, &result.WorkflowStartedAt, &result.StartedAt,
		&result.FinishedAt, &result.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrRunNotFound
	}
	if err != nil {
		return Run{}, fmt.Errorf("scan reconciliation run: %w", err)
	}
	if err := json.Unmarshal(summary, &result.Summary); err != nil {
		return Run{}, fmt.Errorf("decode reconciliation summary: %w", err)
	}
	return result, nil
}

// Execute records a repeatable snapshot of invariants. It is safe for a
// Temporal activity retry: completed runs are returned unchanged, while a
// failed transaction leaves the run eligible for a complete retry.
func (r *Repository) Execute(ctx context.Context, id uuid.UUID) (Run, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return Run{}, fmt.Errorf("begin reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := scanRun(tx.QueryRow(ctx, `
		SELECT `+runColumns+`
		FROM reconciliation_runs WHERE id=$1 FOR UPDATE
	`, id))
	if err != nil {
		return Run{}, err
	}
	if current.Status == "completed" {
		if err := tx.Commit(ctx); err != nil {
			return Run{}, err
		}
		return current, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE reconciliation_runs
		SET status='running',started_at=coalesce(started_at,now()),finished_at=NULL,failure_reason=''
		WHERE id=$1
	`, id); err != nil {
		return Run{}, fmt.Errorf("mark reconciliation running: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM reconciliation_issues WHERE run_id=$1`, id); err != nil {
		return Run{}, fmt.Errorf("clear reconciliation retry issues: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT 'missing_payment_ledger',p.id,
			jsonb_build_object('status',p.status,'amount',p.amount,'currency',p.currency)
		FROM payments p
		WHERE p.status='completed'
		  AND NOT EXISTS (
			SELECT 1 FROM ledger_journals j
			WHERE j.reference_type='payment' AND j.reference_id=p.id
		  )
		UNION ALL
		SELECT 'missing_refund_ledger',r.id,
			jsonb_build_object('payment_id',r.payment_id,'amount',r.amount,'currency',r.currency)
		FROM refunds r
		WHERE r.status='completed'
		  AND NOT EXISTS (
			SELECT 1 FROM ledger_journals j
			WHERE j.reference_type='refund' AND j.reference_id=r.id
		  )
		UNION ALL
		SELECT 'unbalanced_journal',j.reference_id,
			jsonb_build_object(
				'journal_id',j.id,'reference_type',j.reference_type,
				'journal_amount',j.amount,
				'entry_count',count(e.id),
				'debit_total',coalesce(sum(e.amount) FILTER (WHERE e.direction='debit'),0),
				'credit_total',coalesce(sum(e.amount) FILTER (WHERE e.direction='credit'),0)
			)
		FROM ledger_journals j
		LEFT JOIN ledger_entries e ON e.journal_id=j.id
		GROUP BY j.id,j.reference_id,j.reference_type,j.amount
		HAVING count(e.id) < 2
			OR coalesce(sum(e.amount) FILTER (WHERE e.direction='debit'),0) <> j.amount
			OR coalesce(sum(e.amount) FILTER (WHERE e.direction='credit'),0) <> j.amount
		UNION ALL
		SELECT 'over_refunded_payment',p.id,
			jsonb_build_object('payment_amount',p.amount,'refunded_amount',sum(r.amount))
		FROM payments p
		JOIN refunds r ON r.payment_id=p.id AND r.status='completed'
		GROUP BY p.id,p.amount
		HAVING sum(r.amount) > p.amount
		UNION ALL
		SELECT 'payment_ledger_mismatch',p.id,
			jsonb_build_object(
				'journal_id',j.id,'payment_amount',p.amount,'journal_amount',j.amount,
				'payment_currency',p.currency,'journal_currency',j.currency
			)
		FROM payments p
		JOIN ledger_journals j
		  ON j.reference_type='payment' AND j.reference_id=p.id
		WHERE j.payment_id<>p.id OR j.amount<>p.amount OR j.currency<>p.currency
		UNION ALL
		SELECT 'refund_ledger_mismatch',r.id,
			jsonb_build_object(
				'journal_id',j.id,'refund_payment_id',r.payment_id,
				'journal_payment_id',j.payment_id,'refund_amount',r.amount,
				'journal_amount',j.amount,'refund_currency',r.currency,
				'journal_currency',j.currency
			)
		FROM refunds r
		JOIN ledger_journals j
		  ON j.reference_type='refund' AND j.reference_id=r.id
		WHERE j.payment_id<>r.payment_id OR j.amount<>r.amount OR j.currency<>r.currency
		UNION ALL
		SELECT 'orphan_payment_ledger',j.reference_id,
			jsonb_build_object('journal_id',j.id,'payment_id',j.payment_id)
		FROM ledger_journals j
		WHERE j.reference_type='payment'
		  AND NOT EXISTS (SELECT 1 FROM payments p WHERE p.id=j.reference_id)
		UNION ALL
		SELECT 'orphan_refund_ledger',j.reference_id,
			jsonb_build_object('journal_id',j.id,'payment_id',j.payment_id)
		FROM ledger_journals j
		WHERE j.reference_type='refund'
		  AND NOT EXISTS (SELECT 1 FROM refunds r WHERE r.id=j.reference_id)
	`)
	if err != nil {
		return Run{}, fmt.Errorf("query reconciliation issues: %w", err)
	}
	type issueRow struct {
		kind      string
		reference uuid.UUID
		details   []byte
	}
	issues := make([]issueRow, 0)
	for rows.Next() {
		var item issueRow
		if err := rows.Scan(&item.kind, &item.reference, &item.details); err != nil {
			rows.Close()
			return Run{}, fmt.Errorf("scan reconciliation issue: %w", err)
		}
		issues = append(issues, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Run{}, fmt.Errorf("iterate reconciliation issues: %w", err)
	}
	rows.Close()

	summary := make(map[string]int)
	for _, issue := range issues {
		summary[issue.kind]++
		if _, err := tx.Exec(ctx, `
			INSERT INTO reconciliation_issues(run_id,issue_type,reference_id,details)
			VALUES($1,$2,$3,$4)
		`, id, issue.kind, issue.reference, string(issue.details)); err != nil {
			return Run{}, fmt.Errorf("insert reconciliation issue: %w", err)
		}
	}
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return Run{}, fmt.Errorf("encode reconciliation summary: %w", err)
	}
	completed, err := scanRun(tx.QueryRow(ctx, `
		UPDATE reconciliation_runs
		SET status='completed',issue_count=$2,summary=$3,finished_at=now()
		WHERE id=$1
		RETURNING `+runColumns+`
	`, id, len(issues), string(summaryJSON)))
	if err != nil {
		return Run{}, fmt.Errorf("complete reconciliation run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, fmt.Errorf("commit reconciliation run: %w", err)
	}
	return completed, nil
}

func (r *Repository) MarkWorkflowStarted(ctx context.Context, id uuid.UUID) error {
	result, err := r.pool.Exec(ctx, `
		UPDATE reconciliation_runs
		SET workflow_started_at=coalesce(workflow_started_at,now())
		WHERE id=$1
	`, id)
	if err != nil {
		return fmt.Errorf("mark reconciliation workflow started: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrRunNotFound
	}
	return nil
}

func (r *Repository) ListUnstarted(ctx context.Context, limit int) ([]Run, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("%w: limit must be between 1 and 1000", ErrInvalidRun)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+runColumns+`
		FROM reconciliation_runs
		WHERE workflow_started_at IS NULL AND status='pending'
		ORDER BY created_at,id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list unstarted reconciliation workflows: %w", err)
	}
	defer rows.Close()
	runs := make([]Run, 0, limit)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unstarted reconciliation workflows: %w", err)
	}
	return runs, nil
}

func (r *Repository) Fail(ctx context.Context, id uuid.UUID, reason string) error {
	reason = strings.TrimSpace(reason)
	if id == uuid.Nil || reason == "" {
		return fmt.Errorf("%w: run id and failure reason are required", ErrInvalidRun)
	}
	result, err := r.pool.Exec(ctx, `
		UPDATE reconciliation_runs
		SET status='failed',
			failure_reason=CASE WHEN status='failed' THEN failure_reason ELSE $2 END,
			finished_at=coalesce(finished_at,now())
		WHERE id=$1 AND status<>'completed'
	`, id, reason)
	if err != nil {
		return fmt.Errorf("fail reconciliation run: %w", err)
	}
	if result.RowsAffected() == 0 {
		var exists bool
		if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reconciliation_runs WHERE id=$1)`, id).Scan(&exists); err != nil {
			return fmt.Errorf("check reconciliation run existence: %w", err)
		}
		if !exists {
			return ErrRunNotFound
		}
	}
	return nil
}

func (r *Repository) ListIssues(ctx context.Context, runID uuid.UUID) ([]Issue, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id,run_id,issue_type,reference_id,details,created_at
		FROM reconciliation_issues WHERE run_id=$1 ORDER BY issue_type,created_at,id
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("list reconciliation issues: %w", err)
	}
	defer rows.Close()
	issues := make([]Issue, 0)
	for rows.Next() {
		var issue Issue
		if err := rows.Scan(&issue.ID, &issue.RunID, &issue.Type, &issue.ReferenceID, &issue.Details, &issue.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan reconciliation issue: %w", err)
		}
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}
