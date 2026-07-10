package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type adminScheduledJobRepository struct {
	db *sql.DB
}

func NewAdminScheduledJobRepository(db *sql.DB) service.AdminScheduledJobRepository {
	return &adminScheduledJobRepository{db: db}
}

func (r *adminScheduledJobRepository) Create(ctx context.Context, job *service.AdminScheduledJob) (*service.AdminScheduledJob, error) {
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO admin_scheduled_jobs
		    (name, job_type, cron_expression, enabled, payload_json, retention_limit, next_run_at, created_by, created_at, updated_at)
		VALUES
		    ($1,$2,$3,$4,$5,$6,$7,$8,NOW(),NOW())
		RETURNING id, name, job_type, cron_expression, enabled, payload_json, retention_limit, last_run_at, next_run_at, last_status, last_message, created_by, created_at, updated_at
	`, job.Name, job.JobType, job.CronExpression, job.Enabled, job.PayloadJSON, job.RetentionLimit, job.NextRunAt, job.CreatedBy)
	return scanAdminScheduledJob(row)
}

func (r *adminScheduledJobRepository) GetByID(ctx context.Context, id int64) (*service.AdminScheduledJob, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, job_type, cron_expression, enabled, payload_json, retention_limit, last_run_at, next_run_at, last_status, last_message, created_by, created_at, updated_at
		FROM admin_scheduled_jobs
		WHERE id = $1
	`, id)
	return scanAdminScheduledJob(row)
}

func (r *adminScheduledJobRepository) List(ctx context.Context) ([]*service.AdminScheduledJob, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, job_type, cron_expression, enabled, payload_json, retention_limit, last_run_at, next_run_at, last_status, last_message, created_by, created_at, updated_at
		FROM admin_scheduled_jobs
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanAdminScheduledJobs(rows)
}

func (r *adminScheduledJobRepository) ListDue(ctx context.Context, now time.Time) ([]*service.AdminScheduledJob, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, job_type, cron_expression, enabled, payload_json, retention_limit, last_run_at, next_run_at, last_status, last_message, created_by, created_at, updated_at
		FROM admin_scheduled_jobs
		WHERE enabled = true AND next_run_at IS NOT NULL AND next_run_at <= $1
		ORDER BY next_run_at ASC
	`, now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanAdminScheduledJobs(rows)
}

func (r *adminScheduledJobRepository) Update(ctx context.Context, job *service.AdminScheduledJob) (*service.AdminScheduledJob, error) {
	row := r.db.QueryRowContext(ctx, `
		UPDATE admin_scheduled_jobs
		SET name = $2, cron_expression = $3, enabled = $4, payload_json = $5, retention_limit = $6, next_run_at = $7, updated_at = NOW()
		WHERE id = $1
		RETURNING id, name, job_type, cron_expression, enabled, payload_json, retention_limit, last_run_at, next_run_at, last_status, last_message, created_by, created_at, updated_at
	`, job.ID, job.Name, job.CronExpression, job.Enabled, job.PayloadJSON, job.RetentionLimit, job.NextRunAt)
	return scanAdminScheduledJob(row)
}

func (r *adminScheduledJobRepository) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM admin_scheduled_jobs WHERE id = $1`, id)
	return err
}

func (r *adminScheduledJobRepository) UpdateAfterRun(ctx context.Context, id int64, lastRunAt time.Time, nextRunAt time.Time, status, message string) error {
	var next any
	if nextRunAt.IsZero() {
		next = nil
	} else {
		next = nextRunAt
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE admin_scheduled_jobs
		SET last_run_at = $2, next_run_at = $3, last_status = $4, last_message = $5, updated_at = NOW()
		WHERE id = $1
	`, id, lastRunAt, next, status, message)
	return err
}

type adminScheduledJobRunRepository struct {
	db *sql.DB
}

func NewAdminScheduledJobRunRepository(db *sql.DB) service.AdminScheduledJobRunRepository {
	return &adminScheduledJobRunRepository{db: db}
}

func (r *adminScheduledJobRunRepository) Create(ctx context.Context, run *service.AdminScheduledJobRun) (*service.AdminScheduledJobRun, error) {
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO admin_scheduled_job_runs
		    (job_id, trigger_type, status, message, result_json, started_at, finished_at, created_at, triggered_by_user)
		VALUES
		    ($1,$2,$3,$4,$5,$6,$7,NOW(),$8)
		RETURNING id, job_id, trigger_type, status, message, result_json, started_at, finished_at, created_at, triggered_by_user
	`, run.JobID, run.TriggerType, run.Status, run.Message, run.ResultJSON, run.StartedAt, run.FinishedAt, run.TriggeredByUser)
	return scanAdminScheduledJobRun(row)
}

func (r *adminScheduledJobRunRepository) UpdateFinished(ctx context.Context, runID int64, status, message, resultJSON string, finishedAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE admin_scheduled_job_runs
		SET status = $2, message = $3, result_json = $4, finished_at = $5
		WHERE id = $1
	`, runID, status, message, resultJSON, finishedAt)
	return err
}

func (r *adminScheduledJobRunRepository) ListByJobID(ctx context.Context, jobID int64, limit int) ([]*service.AdminScheduledJobRun, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, job_id, trigger_type, status, message, result_json, started_at, finished_at, created_at, triggered_by_user
		FROM admin_scheduled_job_runs
		WHERE job_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanAdminScheduledJobRuns(rows)
}

func (r *adminScheduledJobRunRepository) PruneOldRuns(ctx context.Context, jobID int64, keepCount int) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM admin_scheduled_job_runs
		WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY job_id ORDER BY created_at DESC) AS rn
				FROM admin_scheduled_job_runs
				WHERE job_id = $1
			) ranked
			WHERE rn > $2
		)
	`, jobID, keepCount)
	return err
}

func scanAdminScheduledJob(row interface{ Scan(dest ...any) error }) (*service.AdminScheduledJob, error) {
	item := &service.AdminScheduledJob{}
	err := row.Scan(
		&item.ID, &item.Name, &item.JobType, &item.CronExpression, &item.Enabled, &item.PayloadJSON, &item.RetentionLimit,
		&item.LastRunAt, &item.NextRunAt, &item.LastStatus, &item.LastMessage, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return item, nil
}

func scanAdminScheduledJobs(rows *sql.Rows) ([]*service.AdminScheduledJob, error) {
	var out []*service.AdminScheduledJob
	for rows.Next() {
		item, err := scanAdminScheduledJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanAdminScheduledJobRun(row interface{ Scan(dest ...any) error }) (*service.AdminScheduledJobRun, error) {
	item := &service.AdminScheduledJobRun{}
	err := row.Scan(
		&item.ID, &item.JobID, &item.TriggerType, &item.Status, &item.Message, &item.ResultJSON,
		&item.StartedAt, &item.FinishedAt, &item.CreatedAt, &item.TriggeredByUser,
	)
	if err != nil {
		return nil, err
	}
	return item, nil
}

func scanAdminScheduledJobRuns(rows *sql.Rows) ([]*service.AdminScheduledJobRun, error) {
	var out []*service.AdminScheduledJobRun
	for rows.Next() {
		item, err := scanAdminScheduledJobRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
