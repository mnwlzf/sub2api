package service

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// bpsSelectorRepo 是 OpenAIBPSAccountSelector 的测试桩：只实现选号用到的两个方法。
type bpsSelectorRepo struct {
	groupAccounts []Account
	groupErr      error
	byID          map[int64]*Account
}

func (r *bpsSelectorRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, _ int64, platform string) ([]Account, error) {
	if r.groupErr != nil {
		return nil, r.groupErr
	}
	out := make([]Account, 0, len(r.groupAccounts))
	for _, account := range r.groupAccounts {
		if account.Platform == platform {
			out = append(out, account)
		}
	}
	return out, nil
}

func (r *bpsSelectorRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	account, ok := r.byID[id]
	if !ok {
		return nil, errors.New("account not found")
	}
	return account, nil
}

func newBPSSelectorContext(groupID int64) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("api_key", &APIKey{ID: 7, GroupID: &groupID})
	return c
}

func bpsSelectorAccount(id int64, priority int) Account {
	return Account{
		ID:          id,
		Platform:    PlatformOpenAIBPS,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Priority:    priority,
		Credentials: map[string]any{"access_token": "token-" + string(rune('a'+id))},
	}
}

func TestOpenAIBPSAccountSelectorSelectsLowestPriority(t *testing.T) {
	repo := &bpsSelectorRepo{}
	repo.groupAccounts = []Account{
		bpsSelectorAccount(3, 20),
		bpsSelectorAccount(1, 5),
		bpsSelectorAccount(2, 5),
	}
	selector := NewOpenAIBPSAccountSelector(repo)

	account, err := selector.SelectOpenAIBPSAccount(context.Background(), newBPSSelectorContext(42), "gpt-6-astra")
	require.NoError(t, err)
	require.Equal(t, int64(1), account.ID, "同优先级按 ID 升序，取最小 Priority")
}

func TestOpenAIBPSAccountSelectorSkipsUnusableAccounts(t *testing.T) {
	expiredToken := "header." + base64URL(`{"exp":1000000000}`) + ".sig"
	expired := bpsSelectorAccount(4, 1)
	expired.Credentials = map[string]any{"access_token": expiredToken}

	revokedToken := "token-revoked"
	revoked := bpsSelectorAccount(5, 1)
	revoked.Credentials = map[string]any{"access_token": revokedToken}
	revoked.Extra = map[string]any{OpenAIBPSCredentialStateExtraKey: map[string]any{
		"status":            string(OpenAIBPSCredentialStatusRevoked),
		"token_fingerprint": OpenAIBPSCredentialTokenFingerprint(revokedToken),
		"last_seen_at":      time.Now().UTC().Format(time.RFC3339),
	}}

	disabled := bpsSelectorAccount(6, 1)
	disabled.Schedulable = false

	repo := &bpsSelectorRepo{}
	repo.groupAccounts = []Account{expired, revoked, disabled, bpsSelectorAccount(9, 10)}
	selector := NewOpenAIBPSAccountSelector(repo)

	account, err := selector.SelectOpenAIBPSAccount(context.Background(), newBPSSelectorContext(42), "gpt-6-astra")
	require.NoError(t, err)
	require.Equal(t, int64(9), account.ID)
}

func TestOpenAIBPSAccountSelectorErrors(t *testing.T) {
	selector := NewOpenAIBPSAccountSelector(&bpsSelectorRepo{})

	t.Run("缺少分组", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("api_key", &APIKey{ID: 7})
		_, err := selector.SelectOpenAIBPSAccount(context.Background(), c, "gpt-6-astra")
		require.Error(t, err)
	})

	t.Run("没有候选账号", func(t *testing.T) {
		_, err := selector.SelectOpenAIBPSAccount(context.Background(), newBPSSelectorContext(42), "gpt-6-astra")
		require.Error(t, err)
	})
}

func TestOpenAIBPSAccountSelectorLookupByID(t *testing.T) {
	expected := bpsSelectorAccount(11, 1)
	selector := NewOpenAIBPSAccountSelector(&bpsSelectorRepo{
		byID: map[int64]*Account{11: &expected},
	})
	account, err := selector.OpenAIBPSAccountByID(context.Background(), 11)
	require.NoError(t, err)
	require.Equal(t, int64(11), account.ID)

	// Release 为空操作，不应 panic。
	selector.ReleaseOpenAIBPSAccount(context.Background(), account)
}

// base64URL 编码测试用 JWT payload。
func base64URL(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}
