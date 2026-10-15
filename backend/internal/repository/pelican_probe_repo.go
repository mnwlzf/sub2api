package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// pelicanProbePlanColumns is the full column list for plan scans (incl. secrets).
const pelicanProbePlanColumns = `id, name, group_id, model_id, base_url, api_key_encrypted, prompt,
	test_type, reasoning_effort, judge_model_id,
	cron_expression, enabled, is_public, max_results, last_run_at, next_run_at, created_at, updated_at`

// --- Plan Repository ---

type pelicanProbePlanRepository struct {
	db *sql.DB
}

func NewPelicanProbePlanRepository(db *sql.DB) service.PelicanProbePlanRepository {
	return &pelicanProbePlanRepository{db: db}
}

func (r *pelicanProbePlanRepository) Create(ctx context.Context, plan *service.PelicanProbePlanRow) (*service.PelicanProbePlanRow, error) {
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO pelican_probe_plans (name, group_id, model_id, base_url, api_key_encrypted, prompt,
			test_type, reasoning_effort, judge_model_id,
			cron_expression, enabled, is_public, max_results, next_run_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, NOW(), NOW())
		RETURNING `+pelicanProbePlanColumns+`
	`, plan.Name, plan.GroupID, plan.ModelID, plan.BaseURL, plan.APIKeyEncrypted, plan.Prompt,
		plan.TestType, plan.ReasoningEffort, plan.JudgeModelID,
		plan.CronExpression, plan.Enabled, plan.IsPublic, plan.MaxResults, plan.NextRunAt)
	return scanPelicanProbePlan(row)
}

func (r *pelicanProbePlanRepository) GetByID(ctx context.Context, id int64) (*service.PelicanProbePlanRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+pelicanProbePlanColumns+`
		FROM pelican_probe_plans WHERE id = $1
	`, id)
	return scanPelicanProbePlan(row)
}

func (r *pelicanProbePlanRepository) List(ctx context.Context, groupID *int64) ([]*service.PelicanProbePlanRow, error) {
	if groupID != nil {
		rows, err := r.db.QueryContext(ctx, `
			SELECT `+pelicanProbePlanColumns+`
			FROM pelican_probe_plans WHERE group_id = $1
			ORDER BY created_at DESC
		`, *groupID)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		return scanPelicanProbePlans(rows)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+pelicanProbePlanColumns+`
		FROM pelican_probe_plans
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanPelicanProbePlans(rows)
}

func (r *pelicanProbePlanRepository) ListDue(ctx context.Context, now time.Time) ([]*service.PelicanProbePlanRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+pelicanProbePlanColumns+`
		FROM pelican_probe_plans
		WHERE enabled = true AND next_run_at <= $1
		ORDER BY next_run_at ASC
	`, now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanPelicanProbePlans(rows)
}

func (r *pelicanProbePlanRepository) Update(ctx context.Context, plan *service.PelicanProbePlanRow) (*service.PelicanProbePlanRow, error) {
	row := r.db.QueryRowContext(ctx, `
		UPDATE pelican_probe_plans
		SET name = $2, model_id = $3, base_url = $4, api_key_encrypted = $5, prompt = $6,
			test_type = $7, reasoning_effort = $8, judge_model_id = $9,
			cron_expression = $10, enabled = $11, is_public = $12, max_results = $13, next_run_at = $14, updated_at = NOW()
		WHERE id = $1
		RETURNING `+pelicanProbePlanColumns+`
	`, plan.ID, plan.Name, plan.ModelID, plan.BaseURL, plan.APIKeyEncrypted, plan.Prompt,
		plan.TestType, plan.ReasoningEffort, plan.JudgeModelID,
		plan.CronExpression, plan.Enabled, plan.IsPublic, plan.MaxResults, plan.NextRunAt)
	return scanPelicanProbePlan(row)
}

func (r *pelicanProbePlanRepository) ListPublic(ctx context.Context) ([]*service.PelicanProbePlanRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT p.id, p.name, p.group_id, p.model_id, p.base_url, p.api_key_encrypted, p.prompt,
			p.test_type, p.reasoning_effort, p.judge_model_id,
			p.cron_expression, p.enabled, p.is_public, p.max_results, p.last_run_at, p.next_run_at,
			p.created_at, p.updated_at, g.name
		FROM pelican_probe_plans p
		JOIN groups g ON g.id = p.group_id
		WHERE p.is_public = true AND p.enabled = true
		ORDER BY p.created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var plans []*service.PelicanProbePlanRow
	for rows.Next() {
		p := &service.PelicanProbePlanRow{}
		if err := rows.Scan(
			&p.ID, &p.Name, &p.GroupID, &p.ModelID, &p.BaseURL, &p.APIKeyEncrypted, &p.Prompt,
			&p.TestType, &p.ReasoningEffort, &p.JudgeModelID,
			&p.CronExpression, &p.Enabled, &p.IsPublic, &p.MaxResults,
			&p.LastRunAt, &p.NextRunAt, &p.CreatedAt, &p.UpdatedAt, &p.GroupName,
		); err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

func (r *pelicanProbePlanRepository) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM pelican_probe_plans WHERE id = $1`, id)
	return err
}

func (r *pelicanProbePlanRepository) UpdateAfterRun(ctx context.Context, id int64, lastRunAt time.Time, nextRunAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE pelican_probe_plans SET last_run_at = $2, next_run_at = $3, updated_at = NOW() WHERE id = $1
	`, id, lastRunAt, nextRunAt)
	return err
}

// --- Result Repository ---

type pelicanProbeResultRepository struct {
	db *sql.DB
}

func NewPelicanProbeResultRepository(db *sql.DB) service.PelicanProbeResultRepository {
	return &pelicanProbeResultRepository{db: db}
}

func (r *pelicanProbeResultRepository) Create(ctx context.Context, result *service.PelicanProbeResult) (*service.PelicanProbeResult, error) {
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO pelican_probe_results (plan_id, status, raw_html, verdict, note, reviewed_by, reviewed_at,
			error_message, latency_ms, prompt_tokens, completion_tokens, started_at, finished_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NOW())
		RETURNING id, plan_id, status, raw_html, verdict, note, reviewed_by, reviewed_at,
			error_message, latency_ms, prompt_tokens, completion_tokens, started_at, finished_at, created_at
	`, result.PlanID, result.Status, result.RawHTML, result.Verdict, result.Note, result.ReviewedBy,
		result.ReviewedAt, result.ErrorMessage, result.LatencyMs, result.PromptTokens,
		result.CompletionTokens, result.StartedAt, result.FinishedAt)

	out := &service.PelicanProbeResult{}
	if err := row.Scan(
		&out.ID, &out.PlanID, &out.Status, &out.RawHTML, &out.Verdict, &out.Note, &out.ReviewedBy,
		&out.ReviewedAt, &out.ErrorMessage, &out.LatencyMs, &out.PromptTokens,
		&out.CompletionTokens, &out.StartedAt, &out.FinishedAt, &out.CreatedAt,
	); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *pelicanProbeResultRepository) ListByPlanID(ctx context.Context, planID int64, limit int) ([]*service.PelicanProbeResult, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, plan_id, status, raw_html, verdict, note, reviewed_by, reviewed_at,
			error_message, latency_ms, prompt_tokens, completion_tokens, started_at, finished_at, created_at
		FROM pelican_probe_results
		WHERE plan_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, planID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanPelicanProbeResults(rows)
}

func (r *pelicanProbeResultRepository) ListRecent(ctx context.Context, groupID *int64, limit int) ([]*service.PelicanProbeResult, error) {
	if groupID != nil {
		rows, err := r.db.QueryContext(ctx, `
			SELECT r.id, r.plan_id, r.status, r.raw_html, r.verdict, r.note, r.reviewed_by, r.reviewed_at,
				r.error_message, r.latency_ms, r.prompt_tokens, r.completion_tokens, r.started_at, r.finished_at, r.created_at
			FROM pelican_probe_results r
			JOIN pelican_probe_plans p ON p.id = r.plan_id
			WHERE p.group_id = $1
			ORDER BY r.created_at DESC
			LIMIT $2
		`, *groupID, limit)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		return scanPelicanProbeResults(rows)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, plan_id, status, raw_html, verdict, note, reviewed_by, reviewed_at,
			error_message, latency_ms, prompt_tokens, completion_tokens, started_at, finished_at, created_at
		FROM pelican_probe_results
		ORDER BY created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanPelicanProbeResults(rows)
}

func (r *pelicanProbeResultRepository) ListPublicRecent(ctx context.Context, limit int) ([]*service.PelicanProbeResult, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT r.id, r.plan_id, r.status, r.raw_html, r.verdict, r.note, r.reviewed_by, r.reviewed_at,
			r.error_message, r.latency_ms, r.prompt_tokens, r.completion_tokens, r.started_at, r.finished_at, r.created_at
		FROM pelican_probe_results r
		JOIN pelican_probe_plans p ON p.id = r.plan_id
		WHERE p.is_public = true AND p.enabled = true
		ORDER BY r.created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanPelicanProbeResults(rows)
}

func (r *pelicanProbeResultRepository) Review(ctx context.Context, id int64, verdict, note, reviewer string) (*service.PelicanProbeResult, error) {
	row := r.db.QueryRowContext(ctx, `
		UPDATE pelican_probe_results
		SET verdict = $2, note = $3, reviewed_by = $4, reviewed_at = NOW()
		WHERE id = $1
		RETURNING id, plan_id, status, raw_html, verdict, note, reviewed_by, reviewed_at,
			error_message, latency_ms, prompt_tokens, completion_tokens, started_at, finished_at, created_at
	`, id, verdict, note, reviewer)
	out := &service.PelicanProbeResult{}
	if err := row.Scan(
		&out.ID, &out.PlanID, &out.Status, &out.RawHTML, &out.Verdict, &out.Note, &out.ReviewedBy,
		&out.ReviewedAt, &out.ErrorMessage, &out.LatencyMs, &out.PromptTokens,
		&out.CompletionTokens, &out.StartedAt, &out.FinishedAt, &out.CreatedAt,
	); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *pelicanProbeResultRepository) PruneOldResults(ctx context.Context, planID int64, keepCount int) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM pelican_probe_results
		WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY plan_id ORDER BY created_at DESC) AS rn
				FROM pelican_probe_results
				WHERE plan_id = $1
			) ranked
			WHERE rn > $2
		)
	`, planID, keepCount)
	return err
}

func scanPelicanProbePlan(row scannable) (*service.PelicanProbePlanRow, error) {
	p := &service.PelicanProbePlanRow{}
	if err := row.Scan(
		&p.ID, &p.Name, &p.GroupID, &p.ModelID, &p.BaseURL, &p.APIKeyEncrypted, &p.Prompt,
		&p.TestType, &p.ReasoningEffort, &p.JudgeModelID,
		&p.CronExpression, &p.Enabled, &p.IsPublic, &p.MaxResults,
		&p.LastRunAt, &p.NextRunAt, &p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return p, nil
}

func scanPelicanProbePlans(rows *sql.Rows) ([]*service.PelicanProbePlanRow, error) {
	var plans []*service.PelicanProbePlanRow
	for rows.Next() {
		p, err := scanPelicanProbePlan(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

func scanPelicanProbeResults(rows *sql.Rows) ([]*service.PelicanProbeResult, error) {
	var results []*service.PelicanProbeResult
	for rows.Next() {
		r := &service.PelicanProbeResult{}
		if err := rows.Scan(
			&r.ID, &r.PlanID, &r.Status, &r.RawHTML, &r.Verdict, &r.Note, &r.ReviewedBy,
			&r.ReviewedAt, &r.ErrorMessage, &r.LatencyMs, &r.PromptTokens,
			&r.CompletionTokens, &r.StartedAt, &r.FinishedAt, &r.CreatedAt,
		); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
