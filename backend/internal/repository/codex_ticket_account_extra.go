package repository

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 本文件为既有 accountRepository 追加「票据键」的窄写入能力（同包新文件，不改
// account_repo.go）。存在的意义：
//   - 打票/观测写票据走这里，直接 JSONB 合并，不经过 UpdateExtra 的
//     shouldEnqueueSchedulerOutboxForExtraUpdates 判定——票据键与调度无关，
//     每打一发票都触发一次账号调度 outbox 是纯写放大；
//   - 删除走 JSONB 的 `- key` 运算，只删票据键，不动其它 extra。
//
// service.CodexTicketExtraStore 由本文件的两个方法满足；CodexTicketService 在
// 构造时对 AccountRepository 做类型断言自动启用，无需额外接线。

var _ service.CodexTicketExtraStore = (*accountRepository)(nil)

// UpdateCodexTicketExtra 把 accounts.extra[<key>] 原子合并为 value。
func (r *accountRepository) UpdateCodexTicketExtra(ctx context.Context, accountID int64, key string, value any) error {
	if r == nil || r.client == nil {
		return errors.New("nil account repository")
	}
	payload, err := json.Marshal(map[string]any{key: value})
	if err != nil {
		return err
	}
	result, err := clientFromContext(ctx, r.client).ExecContext(ctx,
		"UPDATE accounts SET extra = COALESCE(extra, '{}'::jsonb) || $1::jsonb, updated_at = NOW() WHERE id = $2 AND deleted_at IS NULL",
		string(payload), accountID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrAccountNotFound
	}
	return nil
}

// RemoveCodexTicketExtra 删除 accounts.extra 中的票据键（失效清理/账号重置用）。
func (r *accountRepository) RemoveCodexTicketExtra(ctx context.Context, accountID int64, key string) error {
	if r == nil || r.client == nil {
		return errors.New("nil account repository")
	}
	result, err := clientFromContext(ctx, r.client).ExecContext(ctx,
		"UPDATE accounts SET extra = COALESCE(extra, '{}'::jsonb) - $1, updated_at = NOW() WHERE id = $2 AND deleted_at IS NULL",
		key, accountID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrAccountNotFound
	}
	return nil
}
