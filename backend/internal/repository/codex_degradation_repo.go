package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ListDueCodexDegradationCheckAccounts 返回已到期（或从未检查过）的 Codex
// 降智检测候选账号，供自动检测运行器逐轮扫描。
//
// 到期时间保存在 accounts.extra.codex_degradation_check.next_check_at（RFC3339
// 文本）。这里只做"格式合法才比较、非法/缺失一律视为到期"的宽松判定，最终
// 是否真的到点由 service 层用解析后的结构再判一次，避免任何脏数据卡死队列。
func (r *accountRepository) ListDueCodexDegradationCheckAccounts(ctx context.Context, now time.Time, limit int) ([]service.Account, error) {
	if limit <= 0 {
		return []service.Account{}, nil
	}
	if r.sql == nil {
		return nil, errors.New("account repository SQL executor not configured")
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT id
		FROM accounts
		WHERE deleted_at IS NULL
			AND status = 'active'
			AND platform = 'openai'
			AND type IN ('oauth', 'setup-token')
			AND (
				COALESCE(extra #>> '{codex_degradation_check,next_check_at}', '') = ''
				OR NOT (extra #>> '{codex_degradation_check,next_check_at}') ~
					'^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,6})?(Z|[+-][0-9]{2}:[0-9]{2})$'
				OR (extra #>> '{codex_degradation_check,next_check_at}')::timestamptz <= $1
			)
		ORDER BY id ASC
		LIMIT $2
	`, now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []service.Account{}, nil
	}
	accounts, err := r.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	// 保持 SQL 的到期顺序（GetByIDs 不保证顺序）。
	byID := make(map[int64]*service.Account, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	ordered := make([]service.Account, 0, len(accounts))
	for _, id := range ids {
		if account := byID[id]; account != nil {
			ordered = append(ordered, *account)
		}
	}
	return ordered, nil
}

// InsertCodexDegradationProbeResult 持久化一条降智探测结果。
func (r *accountRepository) InsertCodexDegradationProbeResult(ctx context.Context, record service.CodexDegradationProbeResultRecord) error {
	if r.sql == nil {
		return errors.New("account repository SQL executor not configured")
	}
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	source := record.Source
	if source == "" {
		source = "manual"
	}
	_, err := r.sql.ExecContext(ctx, `
		INSERT INTO codex_degradation_probe_results (
			account_id, model, status, reason, predicted_model,
			probability, parsed_count, http_status, source, created_at
		) VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, $7, $8, $9, $10)
	`,
		record.AccountID, record.Model, record.Status, record.Reason, record.PredictedModel,
		record.Probability, record.ParsedCount, record.HTTPStatus, source, createdAt.UTC(),
	)
	return err
}

// ListCodexDegradationProbeResults 返回账号最近的探测结果（新→旧）。
func (r *accountRepository) ListCodexDegradationProbeResults(ctx context.Context, accountID int64, limit int) ([]service.CodexDegradationProbeResultRecord, error) {
	if r.sql == nil {
		return nil, errors.New("account repository SQL executor not configured")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT account_id, model, status, COALESCE(reason, ''), COALESCE(predicted_model, ''),
			probability, parsed_count, http_status, source, created_at
		FROM codex_degradation_probe_results
		WHERE account_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2
	`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	records := make([]service.CodexDegradationProbeResultRecord, 0, limit)
	for rows.Next() {
		var record service.CodexDegradationProbeResultRecord
		if err := rows.Scan(
			&record.AccountID, &record.Model, &record.Status, &record.Reason, &record.PredictedModel,
			&record.Probability, &record.ParsedCount, &record.HTTPStatus, &record.Source, &record.CreatedAt,
		); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}
