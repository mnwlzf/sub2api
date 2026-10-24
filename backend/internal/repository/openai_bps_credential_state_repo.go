package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// BPS 凭证状态的条件写入（AccountRepository 同包扩展）。
//
// 认证失败诊断必须绑定"本次请求实际使用的凭证"：请求可能持续数十秒，期间
// 管理员可能替换 Access Token 或修改账号 ID。写入前先读取当前账号凭证并比较
// token 指纹与有效账号 ID，写入 SQL 再以读取到的原始凭证值做条件更新，双重
// 保证迟到响应不会污染新凭证。
var _ service.OpenAIBPSCredentialFailureWriter = (*accountRepository)(nil)

var errOpenAIBPSCredentialStateRepositoryUnavailable = errors.New("openai bps credential state repository is unavailable")

// MarkOpenAIBPSAuthFailure 仅在账号当前凭证仍与本次请求使用的 Access Token 与
// 账号 ID 一致时写入 extra 的 bps_credential_state，返回是否写入。
// 账号不存在、平台不符、凭证已替换或账号 ID 已变更都返回 (false, nil)。
func (r *accountRepository) MarkOpenAIBPSAuthFailure(
	ctx context.Context,
	accountID int64,
	accessToken string,
	accountIDHeader string,
	errorCode string,
) (bool, error) {
	if r == nil || r.sql == nil {
		return false, errOpenAIBPSCredentialStateRepositoryUnavailable
	}
	if accountID <= 0 || strings.TrimSpace(accessToken) == "" {
		return false, nil
	}
	storedToken, storedAccountID, found, err := r.readOpenAIBPSCredentials(ctx, accountID)
	if err != nil || !found {
		return false, err
	}
	// 指纹比较是条件写入的第一道闸：token 已轮换则本次响应必然迟到。
	if service.OpenAIBPSCredentialTokenFingerprint(storedToken) !=
		service.OpenAIBPSCredentialTokenFingerprint(accessToken) {
		return false, nil
	}
	// 账号 ID 覆盖值可能独立于 token 被修改，同样需要比对有效值
	// （credentials 覆盖优先，缺省回退 JWT）。
	currentAccountID := service.ParseOpenAIBPSCredentials(map[string]any{
		"access_token":       storedToken,
		"chatgpt_account_id": storedAccountID,
	}).AccountID
	if currentAccountID != strings.TrimSpace(accountIDHeader) {
		return false, nil
	}

	extra := service.NewOpenAIBPSCredentialStateExtra(
		errorCode,
		service.OpenAIBPSCredentialTokenFingerprint(storedToken),
		currentAccountID,
		time.Now().UTC(),
	)
	payload, err := json.Marshal(extra)
	if err != nil {
		return false, err
	}
	// 条件更新再次以读取到的原始凭证值做匹配：读与写之间的并发修改会被拒绝。
	query := `
		UPDATE accounts
		SET extra = jsonb_set(
				COALESCE(extra, '{}'::jsonb),
				'{` + service.OpenAIBPSCredentialStateExtraKey + `}',
				$1::jsonb,
				true
			),
			updated_at = NOW()
		WHERE id = $2
			AND deleted_at IS NULL
			AND platform = $3
			AND COALESCE(credentials ->> 'access_token', '') = $4
			AND COALESCE(credentials ->> 'chatgpt_account_id', '') = $5
	`
	result, err := r.sql.ExecContext(ctx, query,
		string(payload), accountID, service.PlatformOpenAIBPS, storedToken, storedAccountID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// readOpenAIBPSCredentials 读取账号当前保存的原始凭证值（未归一化）。
// 返回 found=false 表示账号不存在、已软删除或不是 BPS 平台。
func (r *accountRepository) readOpenAIBPSCredentials(ctx context.Context, accountID int64) (string, string, bool, error) {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT
			COALESCE(credentials ->> 'access_token', ''),
			COALESCE(credentials ->> 'chatgpt_account_id', '')
		FROM accounts
		WHERE id = $1
			AND deleted_at IS NULL
			AND platform = $2
	`, accountID, service.PlatformOpenAIBPS)
	if err != nil {
		return "", "", false, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return "", "", false, rows.Err()
	}
	var accessToken, storedAccountID string
	if err := rows.Scan(&accessToken, &storedAccountID); err != nil {
		return "", "", false, err
	}
	return accessToken, storedAccountID, true, nil
}
