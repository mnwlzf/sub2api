package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 本文件实现 Codex 票据生命周期仓储：
//   - CurrentTicket：读 accounts.extra 中的当前票据（管理端诊断用）；
//   - Invalidate：同一事务内「按 generation 去重插入失效事件」+「仅当账号 extra
//     仍是这一代票据时删除」，保证多副本并发观测不会重复记事件、也不会误删新票；
//   - ListInvalidations / GetInvalidation：管理端失效流水查询。

// CodexTicketInvalidationRepository 是 repository 层的票据生命周期仓储实现。
type CodexTicketInvalidationRepository struct {
	db *sql.DB
}

// NewCodexTicketInvalidationRepository 构造票据生命周期仓储。
func NewCodexTicketInvalidationRepository(db *sql.DB) *CodexTicketInvalidationRepository {
	return &CodexTicketInvalidationRepository{db: db}
}

var _ service.CodexTicketLifecycleRepository = (*CodexTicketInvalidationRepository)(nil)

// CurrentTicket 读取 accounts.extra 中的当前票据 JSON；不存在时返回 nil, nil。
func (r *CodexTicketInvalidationRepository) CurrentTicket(ctx context.Context, accountID int64, model string) (json.RawMessage, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("nil codex ticket lifecycle database")
	}
	var raw json.RawMessage
	// 缺失的 JSONB 键是 SQL NULL，无法扫描进 RawMessage，故用 COALESCE 转成 JSON null。
	err := r.db.QueryRowContext(ctx, `SELECT COALESCE(extra->$2, 'null'::jsonb) FROM accounts WHERE id=$1 AND deleted_at IS NULL`,
		accountID, "codex_turn_ticket:"+model).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return nil, nil
	}
	return raw, nil
}

// Invalidate 持久化一条失效事件，并原子删除仍然匹配的当前票据。
// 返回 (是否新插入, error)：同一 generation 的重复事件返回 false（去重）。
func (r *CodexTicketInvalidationRepository) Invalidate(ctx context.Context, event *service.CodexTicketInvalidation) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("nil codex ticket lifecycle database")
	}
	if event == nil || event.TicketGenerationID == "" || event.ReturnedTicket == "" {
		return false, errors.New("invalid codex ticket invalidation")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	key := "codex_turn_ticket:" + event.Model
	var current sql.NullString
	// 行锁保证「读当前代数 + 删除当前票据」与并发写入/打票串行。
	if err := tx.QueryRowContext(ctx, `SELECT extra->$2->>'generation_id' FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`,
		event.AccountID, key).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	cookies := any(nil)
	if len(event.ReturnedSetCookies) > 0 {
		raw, err := json.Marshal(event.ReturnedSetCookies)
		if err != nil {
			return false, err
		}
		cookies = string(raw)
	}
	originalTicket := nullableString(event.OriginalTicket)
	originalCookie := nullableString(event.OriginalCookie)
	returnedCookie := nullableString(event.ReturnedCookie)
	var insertedID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO codex_ticket_invalidations
		(account_id,model,ticket_generation_id,occurred_at,reason_code,response_http_status,request_kind,request_route,invalidated_current,
		 original_ticket,original_cookie,returned_ticket,returned_cookie,returned_set_cookies)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (ticket_generation_id) DO NOTHING
		RETURNING id`,
		event.AccountID, event.Model, event.TicketGenerationID, event.OccurredAt, event.ReasonCode,
		event.ResponseHTTPStatus, event.RequestKind, event.RequestRoute,
		current.Valid && current.String == event.TicketGenerationID,
		originalTicket, originalCookie, event.ReturnedTicket, returnedCookie, cookies).Scan(&insertedID)
	if errors.Is(err, sql.ErrNoRows) {
		// 重复事件：只允许把「同一代票据的更晚观测」补齐到已有行（更完整的上游证据）。
		_, updateErr := tx.ExecContext(ctx, `UPDATE codex_ticket_invalidations SET
			occurred_at=$2,response_http_status=$3,request_kind=$4,request_route=$5,
			original_ticket=COALESCE($6,original_ticket),original_cookie=COALESCE($7,original_cookie),
			returned_ticket=$8,returned_cookie=COALESCE($9,returned_cookie),returned_set_cookies=COALESCE($10,returned_set_cookies)
			WHERE ticket_generation_id=$1 AND occurred_at<$2`,
			event.TicketGenerationID, event.OccurredAt, event.ResponseHTTPStatus, event.RequestKind, event.RequestRoute,
			originalTicket, originalCookie, event.ReturnedTicket, returnedCookie, cookies)
		if updateErr != nil {
			return false, updateErr
		}
		return false, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	invalidatedCurrent := current.Valid && current.String == event.TicketGenerationID
	if invalidatedCurrent {
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET extra=COALESCE(extra,'{}'::jsonb)-$2, updated_at=NOW() WHERE id=$1`,
			event.AccountID, key); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	event.ID, event.InvalidatedCurrent = insertedID, invalidatedCurrent
	return true, nil
}

// ListInvalidations 分页返回失效流水列表（只给存在性布尔，不回传票据）。
func (r *CodexTicketInvalidationRepository) ListInvalidations(ctx context.Context, accountID int64, model string, start, end time.Time, page, size int) ([]service.CodexTicketInvalidationSummary, int64, error) {
	if r == nil || r.db == nil {
		return nil, 0, errors.New("nil codex ticket lifecycle database")
	}
	condition := `account_id=$1 AND ($2='' OR model=$2) AND occurred_at >= GREATEST($3,NOW()-INTERVAL '90 days') AND occurred_at < $4`
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM codex_ticket_invalidations WHERE "+condition,
		accountID, model, start, end).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,model,ticket_generation_id,occurred_at,recorded_at,reason_code,response_http_status,request_kind,request_route,invalidated_current,
		(original_ticket IS NOT NULL),(original_cookie IS NOT NULL),(returned_cookie IS NOT NULL),COALESCE(jsonb_array_length(returned_set_cookies),0)
		FROM codex_ticket_invalidations WHERE `+condition+` ORDER BY occurred_at DESC,id DESC LIMIT $5 OFFSET $6`,
		accountID, model, start, end, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]service.CodexTicketInvalidationSummary, 0, size)
	for rows.Next() {
		item := service.CodexTicketInvalidationSummary{AccountID: accountID}
		var status sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Model, &item.TicketGenerationID, &item.OccurredAt, &item.RecordedAt,
			&item.ReasonCode, &status, &item.RequestKind, &item.RequestRoute, &item.InvalidatedCurrent,
			&item.OriginalTicketPresent, &item.OriginalCookiePresent, &item.ReturnedCookiePresent,
			&item.ReturnedSetCookieCount); err != nil {
			return nil, 0, err
		}
		if status.Valid {
			value := int(status.Int64)
			item.ResponseHTTPStatus = &value
		}
		result = append(result, item)
	}
	return result, total, rows.Err()
}

// GetInvalidation 返回单条失效流水详情。
func (r *CodexTicketInvalidationRepository) GetInvalidation(ctx context.Context, accountID, id int64) (*service.CodexTicketInvalidation, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("nil codex ticket lifecycle database")
	}
	row := r.db.QueryRowContext(ctx, `SELECT id,model,ticket_generation_id,occurred_at,recorded_at,reason_code,response_http_status,request_kind,request_route,invalidated_current,
		original_ticket,original_cookie,returned_ticket,returned_cookie,returned_set_cookies
		FROM codex_ticket_invalidations WHERE account_id=$1 AND id=$2 AND occurred_at >= NOW()-INTERVAL '90 days'`,
		accountID, id)
	item := &service.CodexTicketInvalidation{AccountID: accountID}
	var status sql.NullInt64
	var originalTicket, originalCookie, returnedCookie sql.NullString
	var cookies []byte
	if err := row.Scan(&item.ID, &item.Model, &item.TicketGenerationID, &item.OccurredAt, &item.RecordedAt,
		&item.ReasonCode, &status, &item.RequestKind, &item.RequestRoute, &item.InvalidatedCurrent,
		&originalTicket, &originalCookie, &item.ReturnedTicket, &returnedCookie, &cookies); err != nil {
		return nil, err
	}
	if status.Valid {
		value := int(status.Int64)
		item.ResponseHTTPStatus = &value
	}
	if originalTicket.Valid {
		item.OriginalTicket = &originalTicket.String
	}
	if originalCookie.Valid {
		item.OriginalCookie = &originalCookie.String
	}
	if returnedCookie.Valid {
		item.ReturnedCookie = &returnedCookie.String
	}
	if len(cookies) > 0 {
		if err := json.Unmarshal(cookies, &item.ReturnedSetCookies); err != nil {
			return nil, fmt.Errorf("parse codex ticket invalidation cookies: %w", err)
		}
	}
	return item, nil
}

// nullableString 把 *string 转为 SQL 参数（nil 保持 NULL）。
func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
