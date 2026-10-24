package service

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newOpenAIBPSTestStore(t *testing.T) (*OpenAIBPSSessionStore, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewOpenAIBPSSessionStore(client), server
}

func newOpenAIBPSTestClient(t *testing.T, addr string) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// 没有 Redis 客户端时必须显式报错，绝不能 panic：平台依赖会话绑定，
// 静默降级会让同一会话在并发请求里换号。
func TestOpenAIBPSSessionStoreNilClient(t *testing.T) {
	ctx := context.Background()
	stores := map[string]*OpenAIBPSSessionStore{
		"nil client":  NewOpenAIBPSSessionStore(nil),
		"nil pointer": nil,
	}
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			_, err := store.Bind(ctx, 1, "session", 2, "workspace")
			require.ErrorIs(t, err, ErrOpenAIBPSSessionStoreUnavailable)
			_, _, _, err = store.BoundAccount(ctx, 1, "session")
			require.ErrorIs(t, err, ErrOpenAIBPSSessionStoreUnavailable)
			require.ErrorIs(t, store.Touch(ctx, 1, "session"), ErrOpenAIBPSSessionStoreUnavailable)
		})
	}
}

func TestOpenAIBPSSessionStoreRejectsInvalidIdentity(t *testing.T) {
	store, _ := newOpenAIBPSTestStore(t)
	ctx := context.Background()
	_, err := store.Bind(ctx, 0, "session", 2, "workspace")
	require.Error(t, err)
	_, err = store.Bind(ctx, 1, "  ", 2, "workspace")
	require.Error(t, err)
	_, err = store.Bind(ctx, 1, "session", 0, "workspace")
	require.Error(t, err)
	_, _, _, err = store.BoundAccount(ctx, 0, "session")
	require.Error(t, err)
	require.Error(t, store.Touch(ctx, 1, ""))
}

func TestOpenAIBPSSessionStoreBindAndIsolation(t *testing.T) {
	store, server := newOpenAIBPSTestStore(t)
	ctx := context.Background()

	created, err := store.Bind(ctx, 11, "session-a", 7, "workspace-1")
	require.NoError(t, err)
	require.True(t, created)

	key := OpenAIBPSSessionRedisKey(11, "session-a")
	require.True(t, server.Exists(key))
	require.Equal(t, OpenAIBPSSessionTTL, server.TTL(key), "绑定必须带 7 天 TTL")

	accountID, workspace, ok, err := store.BoundAccount(ctx, 11, "session-a")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(7), accountID)
	require.Equal(t, "workspace-1", workspace)

	// 按 API Key 隔离：同一 sessionKey 在别的租户下读不到。
	accountID, workspace, ok, err = store.BoundAccount(ctx, 12, "session-a")
	require.NoError(t, err)
	require.False(t, ok)
	require.Zero(t, accountID)
	require.Empty(t, workspace)

	// 已有绑定不能被覆盖：Bind 返回 false 且内容保持不变。
	created, err = store.Bind(ctx, 11, "session-a", 99, "workspace-9")
	require.NoError(t, err)
	require.False(t, created)
	accountID, workspace, ok, err = store.BoundAccount(ctx, 11, "session-a")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(7), accountID)
	require.Equal(t, "workspace-1", workspace)
}

func TestOpenAIBPSSessionStoreReadRenewsTTL(t *testing.T) {
	store, server := newOpenAIBPSTestStore(t)
	ctx := context.Background()
	created, err := store.Bind(ctx, 21, "session-b", 3, "workspace-b")
	require.NoError(t, err)
	require.True(t, created)

	server.FastForward(6 * 24 * time.Hour)
	require.Less(t, server.TTL(OpenAIBPSSessionRedisKey(21, "session-b")), OpenAIBPSSessionTTL)

	_, _, ok, err := store.BoundAccount(ctx, 21, "session-b")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, OpenAIBPSSessionTTL, server.TTL(OpenAIBPSSessionRedisKey(21, "session-b")),
		"每次读都必须续期")

	// Touch 同样续期；不存在的键不报错（幂等）。
	server.FastForward(6 * 24 * time.Hour)
	require.NoError(t, store.Touch(ctx, 21, "session-b"))
	require.Equal(t, OpenAIBPSSessionTTL, server.TTL(OpenAIBPSSessionRedisKey(21, "session-b")))
	require.NoError(t, store.Touch(ctx, 21, "missing-session"))
}

func TestOpenAIBPSSessionStoreExpiry(t *testing.T) {
	store, server := newOpenAIBPSTestStore(t)
	ctx := context.Background()
	created, err := store.Bind(ctx, 31, "session-c", 5, "workspace-c")
	require.NoError(t, err)
	require.True(t, created)

	// 7 天闲置后绑定过期：新会话必须重新绑定，而不是复用旧账号。
	server.FastForward(OpenAIBPSSessionTTL + time.Second)
	accountID, workspace, ok, err := store.BoundAccount(ctx, 31, "session-c")
	require.NoError(t, err)
	require.False(t, ok)
	require.Zero(t, accountID)
	require.Empty(t, workspace)

	created, err = store.Bind(ctx, 31, "session-c", 6, "workspace-d")
	require.NoError(t, err)
	require.True(t, created)
	accountID, workspace, ok, err = store.BoundAccount(ctx, 31, "session-c")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(6), accountID)
	require.Equal(t, "workspace-d", workspace)
}

// 两个实例（两个 redis.Client 指向同一 Redis）必须共享绑定：BPS 状态层
// 没有进程内缓存，跨实例换号约束才成立。
func TestOpenAIBPSSessionStoreCrossInstance(t *testing.T) {
	server := miniredis.RunT(t)
	storeA := NewOpenAIBPSSessionStore(newOpenAIBPSTestClient(t, server.Addr()))
	storeB := NewOpenAIBPSSessionStore(newOpenAIBPSTestClient(t, server.Addr()))
	ctx := context.Background()

	created, err := storeA.Bind(ctx, 41, "session-d", 8, "workspace-e")
	require.NoError(t, err)
	require.True(t, created)

	accountID, workspace, ok, err := storeB.BoundAccount(ctx, 41, "session-d")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(8), accountID)
	require.Equal(t, "workspace-e", workspace)

	// 跨实例并发冲突：B 的 Bind 不能覆盖 A 的绑定。
	created, err = storeB.Bind(ctx, 41, "session-d", 9, "workspace-f")
	require.NoError(t, err)
	require.False(t, created)
	accountID, workspace, ok, err = storeA.BoundAccount(ctx, 41, "session-d")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(8), accountID)
	require.Equal(t, "workspace-e", workspace)
}

func TestOpenAIBPSSessionStoreCorruptBinding(t *testing.T) {
	store, server := newOpenAIBPSTestStore(t)
	ctx := context.Background()
	key := OpenAIBPSSessionRedisKey(51, "session-e")
	require.NoError(t, server.Set(key, "not-json"))

	_, _, _, err := store.BoundAccount(ctx, 51, "session-e")
	require.ErrorIs(t, err, ErrOpenAIBPSSessionCorrupt)
	require.False(t, server.Exists(key), "坏记录必须删除，避免会话永久卡死")

	// 坏记录删除后按无绑定处理，可以重新绑定。
	_, _, ok, err := store.BoundAccount(ctx, 51, "session-e")
	require.NoError(t, err)
	require.False(t, ok)
}
