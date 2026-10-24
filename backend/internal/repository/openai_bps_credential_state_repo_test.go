package repository

import (
	"context"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// openAIBPSStatePayloadMatcher 校验写入 extra 的 bps_credential_state 载荷。
type openAIBPSStatePayloadMatcher struct {
	status           service.OpenAIBPSCredentialStatus
	errorCode        string
	accountID        string
	tokenFingerprint string
}

func (m openAIBPSStatePayloadMatcher) Match(value driver.Value) bool {
	raw, ok := value.(string)
	if !ok {
		return false
	}
	var payload struct {
		Status           service.OpenAIBPSCredentialStatus `json:"status"`
		LastSeenAt       string                            `json:"last_seen_at"`
		ErrorCode        string                            `json:"error_code"`
		TokenFingerprint string                            `json:"token_fingerprint"`
		AccountID        string                            `json:"account_id"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return false
	}
	if m.status != "" && payload.Status != m.status {
		return false
	}
	if m.errorCode != "" && payload.ErrorCode != m.errorCode {
		return false
	}
	if payload.AccountID != m.accountID {
		return false
	}
	if payload.TokenFingerprint != m.tokenFingerprint {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, payload.LastSeenAt); err != nil {
		return false
	}
	return true
}

// openAIBPSRepoTestJWT 构造用于仓库层测试的 JWT。
func openAIBPSRepoTestJWT(t *testing.T, accountID string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
	})
	require.NoError(t, err)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func expectOpenAIBPSCredentialRead(mock sqlmock.Sqlmock, accountID int64, accessToken, storedAccountID string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(credentials ->> 'access_token', '')")).
		WithArgs(accountID, service.PlatformOpenAIBPS).
		WillReturnRows(sqlmock.NewRows([]string{"access_token", "chatgpt_account_id"}).
			AddRow(accessToken, storedAccountID))
}

func expectOpenAIBPSStateWrite(
	mock sqlmock.Sqlmock,
	accountID int64,
	storedToken, storedAccountID string,
	matcher openAIBPSStatePayloadMatcher,
	affected int64,
) {
	mock.ExpectExec(regexp.QuoteMeta("UPDATE accounts SET extra = jsonb_set(")).
		WithArgs(matcher, accountID, service.PlatformOpenAIBPS, storedToken, storedAccountID).
		WillReturnResult(sqlmock.NewResult(0, affected))
}

func TestOpenAIBPSMarkAuthFailureWritesMatchingCredentials(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	expectOpenAIBPSCredentialRead(mock, 7, "at-current", "acc-workspace")
	expectOpenAIBPSStateWrite(mock, 7, "at-current", "acc-workspace", openAIBPSStatePayloadMatcher{
		status:           service.OpenAIBPSCredentialStatusRevoked,
		errorCode:        "token_revoked",
		accountID:        "acc-workspace",
		tokenFingerprint: service.OpenAIBPSCredentialTokenFingerprint("at-current"),
	}, 1)

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	written, err := repo.MarkOpenAIBPSAuthFailure(context.Background(), 7, "at-current", "acc-workspace", "token_revoked")
	require.NoError(t, err)
	require.True(t, written)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIBPSMarkAuthFailureMapsNonRevokedCodeToAuthFailed(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	expectOpenAIBPSCredentialRead(mock, 8, "at-current", "acc")
	expectOpenAIBPSStateWrite(mock, 8, "at-current", "acc", openAIBPSStatePayloadMatcher{
		status:           service.OpenAIBPSCredentialStatusAuthFailed,
		errorCode:        "invalid_token",
		accountID:        "acc",
		tokenFingerprint: service.OpenAIBPSCredentialTokenFingerprint("at-current"),
	}, 1)

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	written, err := repo.MarkOpenAIBPSAuthFailure(context.Background(), 8, "at-current", "acc", "invalid_token")
	require.NoError(t, err)
	require.True(t, written)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIBPSMarkAuthFailureRejectsRotatedToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// 账号当前 token 已被管理员替换；本次请求使用的是旧 token。
	expectOpenAIBPSCredentialRead(mock, 7, "at-new", "acc-workspace")

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	written, err := repo.MarkOpenAIBPSAuthFailure(context.Background(), 7, "at-old", "acc-workspace", "token_revoked")
	require.NoError(t, err)
	require.False(t, written)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIBPSMarkAuthFailureRejectsChangedAccountID(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	expectOpenAIBPSCredentialRead(mock, 7, "at-current", "acc-new")

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	written, err := repo.MarkOpenAIBPSAuthFailure(context.Background(), 7, "at-current", "acc-old", "token_revoked")
	require.NoError(t, err)
	require.False(t, written)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIBPSMarkAuthFailureLateResponseIsDropped(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// 读取后、写入前凭证被替换：条件更新影响 0 行，迟到响应被丢弃。
	expectOpenAIBPSCredentialRead(mock, 7, "at-current", "acc")
	expectOpenAIBPSStateWrite(mock, 7, "at-current", "acc", openAIBPSStatePayloadMatcher{
		status:           service.OpenAIBPSCredentialStatusRevoked,
		errorCode:        "token_revoked",
		accountID:        "acc",
		tokenFingerprint: service.OpenAIBPSCredentialTokenFingerprint("at-current"),
	}, 0)

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	written, err := repo.MarkOpenAIBPSAuthFailure(context.Background(), 7, "at-current", "acc", "token_revoked")
	require.NoError(t, err)
	require.False(t, written)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIBPSMarkAuthFailureFallsBackToJWTAccountID(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	token := openAIBPSRepoTestJWT(t, "acc-jwt")
	expectOpenAIBPSCredentialRead(mock, 9, token, "")
	expectOpenAIBPSStateWrite(mock, 9, token, "", openAIBPSStatePayloadMatcher{
		status:           service.OpenAIBPSCredentialStatusRevoked,
		errorCode:        "token_revoked",
		accountID:        "acc-jwt",
		tokenFingerprint: service.OpenAIBPSCredentialTokenFingerprint(token),
	}, 1)

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	written, err := repo.MarkOpenAIBPSAuthFailure(context.Background(), 9, token, "acc-jwt", "token_revoked")
	require.NoError(t, err)
	require.True(t, written)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIBPSMarkAuthFailureToleratesBearerPrefixedStorage(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// 历史数据可能保存了 "Bearer " 前缀；归一化后的指纹仍应匹配。
	expectOpenAIBPSCredentialRead(mock, 7, "Bearer at-current", "acc")
	expectOpenAIBPSStateWrite(mock, 7, "Bearer at-current", "acc", openAIBPSStatePayloadMatcher{
		status:           service.OpenAIBPSCredentialStatusRevoked,
		errorCode:        "token_revoked",
		accountID:        "acc",
		tokenFingerprint: service.OpenAIBPSCredentialTokenFingerprint("at-current"),
	}, 1)

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	written, err := repo.MarkOpenAIBPSAuthFailure(context.Background(), 7, "at-current", "acc", "token_revoked")
	require.NoError(t, err)
	require.True(t, written)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIBPSMarkAuthFailureSkipsMissingAccount(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(credentials ->> 'access_token', '')")).
		WithArgs(int64(404), service.PlatformOpenAIBPS).
		WillReturnRows(sqlmock.NewRows([]string{"access_token", "chatgpt_account_id"}))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	written, err := repo.MarkOpenAIBPSAuthFailure(context.Background(), 404, "at-current", "acc", "token_revoked")
	require.NoError(t, err)
	require.False(t, written)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIBPSMarkAuthFailureSkipsEmptyRequestIdentity(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	written, err := repo.MarkOpenAIBPSAuthFailure(context.Background(), 7, "  ", "acc", "token_revoked")
	require.NoError(t, err)
	require.False(t, written)
	written, err = repo.MarkOpenAIBPSAuthFailure(context.Background(), 0, "at-current", "acc", "token_revoked")
	require.NoError(t, err)
	require.False(t, written)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIBPSMarkAuthFailurePropagatesQueryError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	queryErr := errors.New("query failed")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(credentials ->> 'access_token', '')")).
		WithArgs(int64(7), service.PlatformOpenAIBPS).
		WillReturnError(queryErr)

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	written, err := repo.MarkOpenAIBPSAuthFailure(context.Background(), 7, "at-current", "acc", "token_revoked")
	require.ErrorIs(t, err, queryErr)
	require.False(t, written)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIBPSMarkAuthFailurePropagatesUpdateError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	updateErr := errors.New("update failed")
	expectOpenAIBPSCredentialRead(mock, 7, "at-current", "acc")
	mock.ExpectExec(regexp.QuoteMeta("UPDATE accounts SET extra = jsonb_set(")).
		WithArgs(sqlmock.AnyArg(), int64(7), service.PlatformOpenAIBPS, "at-current", "acc").
		WillReturnError(updateErr)

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	written, err := repo.MarkOpenAIBPSAuthFailure(context.Background(), 7, "at-current", "acc", "token_revoked")
	require.ErrorIs(t, err, updateErr)
	require.False(t, written)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAIBPSMarkAuthFailureUnavailableRepository(t *testing.T) {
	repo := newAccountRepositoryWithSQL(nil, nil, nil)
	written, err := repo.MarkOpenAIBPSAuthFailure(context.Background(), 7, "at-current", "acc", "token_revoked")
	require.Error(t, err)
	require.False(t, written)
}
