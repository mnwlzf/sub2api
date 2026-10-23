package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// 本文件实现 Codex 打票的流水仓储（attempt / event / recent 三种窄接口共用同一实例）。
// 只存元数据：票据本体、OAuth 凭证、代理 URL 一律不落库（见 migrations/251）。
// 事件时间线由 attempt 表与 invalidation 表 UNION 得到，不额外建表。

// CodexTicketAttemptRepository 是 repository 层的打票流水仓储实现。
type CodexTicketAttemptRepository struct {
	db *sql.DB
}

// NewCodexTicketAttemptRepository 构造打票流水仓储。
// 同一实例同时实现 service.CodexTicketAttemptRepository / EventRepository / RecentRepository。
func NewCodexTicketAttemptRepository(db *sql.DB) *CodexTicketAttemptRepository {
	return &CodexTicketAttemptRepository{db: db}
}

var (
	_ service.CodexTicketAttemptRepository = (*CodexTicketAttemptRepository)(nil)
	_ service.CodexTicketEventRepository   = (*CodexTicketAttemptRepository)(nil)
	_ service.CodexTicketRecentRepository  = (*CodexTicketAttemptRepository)(nil)
)

// Insert 写入一条打票流水。成功流水的 ticket_generation_id 有唯一索引，
// 重复写入（并发探测/重试）会被静默忽略，不返回错误。
func (r *CodexTicketAttemptRepository) Insert(ctx context.Context, attempt *service.CodexTicketAttempt) error {
	if r == nil || r.db == nil {
		return errors.New("nil codex ticket attempt database")
	}
	if attempt == nil || attempt.AccountID <= 0 || attempt.Model == "" {
		return errors.New("invalid codex ticket attempt")
	}
	err := r.db.QueryRowContext(ctx, `INSERT INTO codex_ticket_attempts
		(account_id,model,occurred_at,outcome,source,http_status,ticket_length,duration_ms,reason_code,proxy_id,proxy_name,expires_at,
		 ticket_generation_id,verification_method,turn_state_present,cookie_present,challenge_expected_count,parsed_number_count)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT DO NOTHING
		RETURNING id`,
		attempt.AccountID, attempt.Model, attempt.OccurredAt, attempt.Outcome, attempt.Trigger,
		attempt.HTTPStatus, attempt.TicketLength, attempt.DurationMS, attempt.ReasonCode,
		attempt.ProxyID, attempt.ProxyName, attempt.ExpiresAt,
		attempt.TicketGenerationID, attempt.VerificationMethod, attempt.TurnStatePresent, attempt.CookiePresent,
		attempt.ChallengeExpectedCount, attempt.ParsedNumberCount).Scan(&attempt.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

// List 分页返回账号打票流水（默认保留窗口 90 天）。
func (r *CodexTicketAttemptRepository) List(ctx context.Context, accountID int64, model string, successOnly bool, page, size int) ([]service.CodexTicketAttempt, int64, error) {
	if r == nil || r.db == nil {
		return nil, 0, errors.New("nil codex ticket attempt database")
	}
	condition := "account_id=$1 AND ($2='' OR model=$2) AND occurred_at >= NOW() - INTERVAL '90 days'"
	if successOnly {
		condition += " AND outcome='success'"
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM codex_ticket_attempts WHERE "+condition, accountID, model).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,model,occurred_at,outcome,source,http_status,ticket_length,duration_ms,reason_code,proxy_id,proxy_name,expires_at,
		 ticket_generation_id,verification_method,turn_state_present,cookie_present,challenge_expected_count,parsed_number_count
		FROM codex_ticket_attempts WHERE `+condition+` ORDER BY occurred_at DESC,id DESC LIMIT $3 OFFSET $4`,
		accountID, model, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]service.CodexTicketAttempt, 0, size)
	for rows.Next() {
		attempt := service.CodexTicketAttempt{AccountID: accountID, Model: model}
		var status, ticketLength, proxyID, expectedCount, parsedCount sql.NullInt64
		var reason, proxyName sql.NullString
		var expiresAt sql.NullTime
		var generation, verificationMethod sql.NullString
		var turnStatePresent, cookiePresent sql.NullBool
		if err := rows.Scan(&attempt.ID, &attempt.Model, &attempt.OccurredAt, &attempt.Outcome, &attempt.Trigger,
			&status, &ticketLength, &attempt.DurationMS, &reason, &proxyID, &proxyName, &expiresAt,
			&generation, &verificationMethod, &turnStatePresent, &cookiePresent, &expectedCount, &parsedCount); err != nil {
			return nil, 0, err
		}
		if status.Valid {
			value := int(status.Int64)
			attempt.HTTPStatus = &value
		}
		if ticketLength.Valid {
			value := int(ticketLength.Int64)
			attempt.TicketLength = &value
		}
		if proxyID.Valid {
			value := proxyID.Int64
			attempt.ProxyID = &value
		}
		if expiresAt.Valid {
			value := expiresAt.Time
			attempt.ExpiresAt = &value
		}
		if generation.Valid {
			value := generation.String
			attempt.TicketGenerationID = &value
		}
		if turnStatePresent.Valid {
			value := turnStatePresent.Bool
			attempt.TurnStatePresent = &value
		}
		if cookiePresent.Valid {
			value := cookiePresent.Bool
			attempt.CookiePresent = &value
		}
		if expectedCount.Valid {
			value := int(expectedCount.Int64)
			attempt.ChallengeExpectedCount = &value
		}
		if parsedCount.Valid {
			value := int(parsedCount.Int64)
			attempt.ParsedNumberCount = &value
		}
		attempt.ReasonCode, attempt.ProxyName = reason.String, proxyName.String
		attempt.VerificationMethod = verificationMethod.String
		result = append(result, attempt)
	}
	return result, total, rows.Err()
}

// Cleanup 分批删除 90 天前的流水与失效事件。
func (r *CodexTicketAttemptRepository) Cleanup(ctx context.Context) error {
	if r == nil || r.db == nil {
		return errors.New("nil codex ticket attempt database")
	}
	for {
		result, err := r.db.ExecContext(ctx, `DELETE FROM codex_ticket_attempts WHERE id IN (
			SELECT id FROM codex_ticket_attempts WHERE occurred_at < NOW() - INTERVAL '90 days' ORDER BY occurred_at,id LIMIT 1000)`)
		if err != nil {
			return fmt.Errorf("cleanup codex ticket attempts: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected < 1000 {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	for {
		result, err := r.db.ExecContext(ctx, `DELETE FROM codex_ticket_invalidations WHERE id IN (
			SELECT id FROM codex_ticket_invalidations WHERE occurred_at < NOW() - INTERVAL '90 days' ORDER BY occurred_at,id LIMIT 1000)`)
		if err != nil {
			return fmt.Errorf("cleanup codex ticket invalidations: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected < 1000 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

// TryLock 用 PostgreSQL advisory lock 做跨副本的「同一 (账号, 模型) 只允许一发探测」互斥。
func (r *CodexTicketAttemptRepository) TryLock(ctx context.Context, accountID int64, model string) (func(), bool, error) {
	if r == nil || r.db == nil {
		return nil, false, errors.New("nil codex ticket attempt database")
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "codex-ticket:%d:%s", accountID, model)
	key := int64(h.Sum64())
	var acquired bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, false, err
	}
	if !acquired {
		_ = conn.Close()
		return nil, false, nil
	}
	unlock := func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		_ = conn.QueryRowContext(unlockCtx, "SELECT pg_advisory_unlock($1)", key).Scan(&unlocked)
		_ = conn.Close()
	}
	return unlock, true, nil
}

// ListEvents 返回 attempt + invalidation 合并时间线。
func (r *CodexTicketAttemptRepository) ListEvents(ctx context.Context, accountID int64, model, filter string, start, end time.Time, page, size int) ([]service.CodexTicketEvent, int64, error) {
	if r == nil || r.db == nil {
		return nil, 0, errors.New("nil codex ticket attempt database")
	}
	if filter == "" {
		filter = "all"
	}
	if filter != "all" {
		return r.listFilteredEvents(ctx, accountID, model, filter, start, end, page, size)
	}
	const attemptPayload = `to_jsonb(a) - 'source' || jsonb_build_object('trigger', a.source, 'kind', a.outcome)`
	const invalidationPayload = `jsonb_build_object('id',i.id,'account_id',i.account_id,'model',i.model,'kind','invalidation',
		'occurred_at',i.occurred_at,'recorded_at',i.recorded_at,'ticket_generation_id',i.ticket_generation_id,
		'reason_code',i.reason_code,'response_http_status',i.response_http_status,'request_kind',i.request_kind,
		'request_route',i.request_route,'invalidated_current',i.invalidated_current,
		'original_ticket_present',i.original_ticket IS NOT NULL,'original_cookie_present',i.original_cookie IS NOT NULL,
		'returned_cookie_present',i.returned_cookie IS NOT NULL,'returned_set_cookie_count',COALESCE(jsonb_array_length(i.returned_set_cookies),0))`
	query := `WITH events AS (
		SELECT a.id, a.occurred_at, a.model, a.outcome AS kind, ` + attemptPayload + ` AS payload
		FROM codex_ticket_attempts a
		WHERE a.account_id=$1 AND a.occurred_at >= $2 AND a.occurred_at < $3 AND ($4='' OR a.model=$4)
		UNION ALL
		SELECT i.id, i.occurred_at, i.model, 'invalidation' AS kind, ` + invalidationPayload + ` AS payload
		FROM codex_ticket_invalidations i
		WHERE i.account_id=$1 AND i.occurred_at >= $2 AND i.occurred_at < $3 AND ($4='' OR i.model=$4)
	)
	SELECT payload FROM events ORDER BY occurred_at DESC, id DESC, kind LIMIT $5 OFFSET $6`
	countQuery := `SELECT
		(SELECT COUNT(*) FROM codex_ticket_attempts WHERE account_id=$1 AND occurred_at >= $2 AND occurred_at < $3 AND ($4='' OR model=$4)) +
		(SELECT COUNT(*) FROM codex_ticket_invalidations WHERE account_id=$1 AND occurred_at >= $2 AND occurred_at < $3 AND ($4='' OR model=$4))`
	var total int64
	if err := r.db.QueryRowContext(ctx, countQuery, accountID, start, end, model).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, query, accountID, start, end, model, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	result, err := scanCodexTicketEvents(rows, size)
	return result, total, err
}

// listFilteredEvents 处理 attempts/success/failure/invalidation 过滤。
func (r *CodexTicketAttemptRepository) listFilteredEvents(ctx context.Context, accountID int64, model, filter string, start, end time.Time, page, size int) ([]service.CodexTicketEvent, int64, error) {
	table := "codex_ticket_attempts"
	if filter == "invalidation" {
		table = "codex_ticket_invalidations"
	} else if filter != "attempts" && filter != "success" && filter != "failure" {
		return nil, 0, fmt.Errorf("invalid codex ticket event filter")
	}
	condition := "account_id=$1 AND occurred_at >= $2 AND occurred_at < $3"
	args := []any{accountID, start, end}
	if model != "" {
		args = append(args, model)
		condition += " AND model=$4"
	}
	if filter == "success" {
		condition += " AND outcome='success'"
	} else if filter == "failure" {
		condition += " AND outcome IN ('miss','error')"
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE "+condition, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "occurred_at DESC,id DESC"
	payload := `jsonb_build_object('id',i.id,'account_id',i.account_id,'model',i.model,'kind','invalidation',
		'occurred_at',i.occurred_at,'recorded_at',i.recorded_at,'ticket_generation_id',i.ticket_generation_id,
		'reason_code',i.reason_code,'response_http_status',i.response_http_status,'request_kind',i.request_kind,
		'request_route',i.request_route,'invalidated_current',i.invalidated_current,
		'original_ticket_present',i.original_ticket IS NOT NULL,'original_cookie_present',i.original_cookie IS NOT NULL,
		'returned_cookie_present',i.returned_cookie IS NOT NULL,'returned_set_cookie_count',COALESCE(jsonb_array_length(i.returned_set_cookies),0))`
	alias := "i"
	if filter != "invalidation" {
		order += ",outcome"
		alias = "a"
		payload = `to_jsonb(a) - 'source' || jsonb_build_object('trigger', a.source, 'kind', a.outcome)`
	}
	query := fmt.Sprintf(`SELECT %s AS payload FROM (
		SELECT * FROM %s WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d
	) %s ORDER BY %s`, payload, table, condition, order, len(args)+1, len(args)+2, alias, order)
	rows, err := r.db.QueryContext(ctx, query, append(args, size, (page-1)*size)...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	result, err := scanCodexTicketEvents(rows, size)
	return result, total, err
}

func scanCodexTicketEvents(rows *sql.Rows, size int) ([]service.CodexTicketEvent, error) {
	result := make([]service.CodexTicketEvent, 0, size)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var event service.CodexTicketEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// LatestEvents 批量返回账号最近一次打票/失效事件（账号列表页）。
func (r *CodexTicketAttemptRepository) LatestEvents(ctx context.Context, accountIDs []int64) (map[int64]service.CodexTicketRecentEvent, error) {
	result := make(map[int64]service.CodexTicketRecentEvent)
	if r == nil || r.db == nil || len(accountIDs) == 0 {
		return result, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT ON (account_id) account_id,model,kind,occurred_at FROM (
		SELECT account_id,model,outcome AS kind,occurred_at,id FROM codex_ticket_attempts
		WHERE account_id=ANY($1) AND occurred_at >= NOW()-INTERVAL '90 days'
		UNION ALL
		SELECT account_id,model,'invalidation' AS kind,occurred_at,id FROM codex_ticket_invalidations
		WHERE account_id=ANY($1) AND occurred_at >= NOW()-INTERVAL '90 days'
	) events ORDER BY account_id,occurred_at DESC,id DESC,kind`, pq.Array(accountIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var event service.CodexTicketRecentEvent
		var accountID int64
		if err := rows.Scan(&accountID, &event.Model, &event.Kind, &event.OccurredAt); err != nil {
			return nil, err
		}
		result[accountID] = event
	}
	return result, rows.Err()
}
