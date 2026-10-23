package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// OpenAIBPS 会话状态层：把「BPS 会话 → 上游账号/workspace」的绑定存进 Redis。
//
// 设计约束（见 openspec design.md D6）：
//   - 键按 API Key 隔离：同一段客户端 session_id 在不同租户下必须是不同键；
//   - 绑定必须原子（SET NX）：并发的新会话只能有一个账号赢得绑定，后续请求
//     一律复用绑定账号，不允许静默换号；
//   - 7 天闲置过期，每次读（以及绑定尝试）都续期；
//   - 跨实例共享：不提供进程内降级，Redis 不可用即平台不可用（明确报错）。
const (
	// OpenAIBPSSessionTTL 是会话绑定的闲置过期时间；每次 BoundAccount/Bind
	// 命中都会把它重置回该值。
	OpenAIBPSSessionTTL = 7 * 24 * time.Hour

	openAIBPSSessionKeyPrefix = "bps:session:"
)

// ErrOpenAIBPSSessionStoreUnavailable 表示会话存储不可用（未注入 Redis 客户端）。
// 该错误是显式失败而不是降级：BPS 平台依赖会话绑定做换号约束，没有 Redis
// 就无法保证同一会话不会被并发换到别的账号上。
var ErrOpenAIBPSSessionStoreUnavailable = errors.New("openai bps session store: redis client is nil")

// ErrOpenAIBPSSessionCorrupt 表示键存在但记录无法解析。这类记录视为上下文
// 已损坏：调用方应返回 bps_context_expired，而不是静默重新绑定（静默重绑会
// 在会话中途换号）。
var ErrOpenAIBPSSessionCorrupt = errors.New("openai bps session store: session binding is corrupt")

// openAIBPSSessionRecord 是 Redis 中保存的绑定内容。
type openAIBPSSessionRecord struct {
	AccountID int64  `json:"account_id"`
	Workspace string `json:"workspace,omitempty"`
	BoundAt   int64  `json:"bound_at,omitempty"`
}

// OpenAIBPSSessionStore 是 BPS 会话绑定的 Redis 实现。
//
// 零值不可用：必须用 NewOpenAIBPSSessionStore 构造。client 为 nil 时所有方法
// 返回 ErrOpenAIBPSSessionStoreUnavailable，绝不 panic。
type OpenAIBPSSessionStore struct {
	client *redis.Client
}

// NewOpenAIBPSSessionStore 用共享 Redis 客户端构造会话存储。
// client 允许为 nil（平台关闭/单测场景），此时所有方法显式返回错误。
func NewOpenAIBPSSessionStore(client *redis.Client) *OpenAIBPSSessionStore {
	return &OpenAIBPSSessionStore{client: client}
}

// OpenAIBPSSessionRedisKey 返回会话绑定的 Redis 键：bps:session:<apiKeyID>:<sessionKey>。
// sessionKey 应是 OpenAIBPSSessionKey 派生出的不可猜测摘要；apiKeyID 显式入键，
// 保证租户隔离在键结构上可见、便于运维排查。
func OpenAIBPSSessionRedisKey(apiKeyID int64, sessionKey string) string {
	return openAIBPSSessionKeyPrefix + strconv.FormatInt(apiKeyID, 10) + ":" + strings.TrimSpace(sessionKey)
}

func (s *OpenAIBPSSessionStore) available() error {
	if s == nil || s.client == nil {
		return ErrOpenAIBPSSessionStoreUnavailable
	}
	return nil
}

func openAIBPSSessionKeyFor(apiKeyID int64, sessionKey string) (string, error) {
	sessionKey = strings.TrimSpace(sessionKey)
	if apiKeyID <= 0 {
		return "", fmt.Errorf("openai bps session store: invalid api key id %d", apiKeyID)
	}
	if sessionKey == "" {
		return "", errors.New("openai bps session store: session key is empty")
	}
	return OpenAIBPSSessionRedisKey(apiKeyID, sessionKey), nil
}

// Bind 尝试为会话建立账号绑定。
//
// 语义是 SET NX 原子绑定：
//   - 键不存在：写入绑定并返回 (true, nil)；
//   - 键已存在：不改写绑定，只续期 TTL，返回 (false, nil) —— 调用方必须
//     重新读取 BoundAccount 并以既有绑定为准（并发冲突时不能覆盖赢家）。
//
// workspace 是绑定时的 chatgpt-account-id 身份；读取方用它校验账号凭据没有
// 漂移（同一账号换了 workspace 时该会话应判定为不可用而不是继续发送）。
func (s *OpenAIBPSSessionStore) Bind(ctx context.Context, apiKeyID int64, sessionKey string, accountID int64, workspace string) (bool, error) {
	if err := s.available(); err != nil {
		return false, err
	}
	if accountID <= 0 {
		return false, fmt.Errorf("openai bps session store: invalid account id %d", accountID)
	}
	key, err := openAIBPSSessionKeyFor(apiKeyID, sessionKey)
	if err != nil {
		return false, err
	}
	record, err := json.Marshal(openAIBPSSessionRecord{
		AccountID: accountID,
		Workspace: strings.TrimSpace(workspace),
		BoundAt:   time.Now().Unix(),
	})
	if err != nil {
		return false, fmt.Errorf("openai bps session store: encode binding: %w", err)
	}
	created, err := s.client.SetNX(ctx, key, record, OpenAIBPSSessionTTL).Result()
	if err != nil {
		return false, fmt.Errorf("openai bps session store: bind: %w", err)
	}
	if created {
		return true, nil
	}
	// 已存在绑定：保持账号不变，只续期。这里不重试 SetNX：即便在 SET NX 与
	// EXPIRE 之间键过期，也应由下一次请求重新走绑定流程，而不是覆盖别人的键。
	if err := s.client.Expire(ctx, key, OpenAIBPSSessionTTL).Err(); err != nil {
		return false, fmt.Errorf("openai bps session store: refresh binding ttl: %w", err)
	}
	return false, nil
}

// BoundAccount 读取会话当前绑定的账号。ok=false 表示没有绑定（新会话或已过期）。
// 命中时同时续期 TTL（GETEX），因此活跃会话不会在 7 天内过期。
// 记录损坏时返回 ErrOpenAIBPSSessionCorrupt（并删除坏键，避免永久卡死）。
func (s *OpenAIBPSSessionStore) BoundAccount(ctx context.Context, apiKeyID int64, sessionKey string) (accountID int64, workspace string, ok bool, err error) {
	if err := s.available(); err != nil {
		return 0, "", false, err
	}
	key, err := openAIBPSSessionKeyFor(apiKeyID, sessionKey)
	if err != nil {
		return 0, "", false, err
	}
	data, err := s.client.GetEx(ctx, key, OpenAIBPSSessionTTL).Bytes()
	if errors.Is(err, redis.Nil) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, fmt.Errorf("openai bps session store: read binding: %w", err)
	}
	var record openAIBPSSessionRecord
	if err := json.Unmarshal(data, &record); err != nil || record.AccountID <= 0 {
		_ = s.client.Del(ctx, key).Err()
		return 0, "", false, ErrOpenAIBPSSessionCorrupt
	}
	return record.AccountID, record.Workspace, true, nil
}

// Touch 仅续期已存在绑定的 TTL。键不存在时不是错误（幂等），供调用方在
// 长连接/日志路径上显式保活；BoundAccount 与 Bind 本身已经续期。
func (s *OpenAIBPSSessionStore) Touch(ctx context.Context, apiKeyID int64, sessionKey string) error {
	if err := s.available(); err != nil {
		return err
	}
	key, err := openAIBPSSessionKeyFor(apiKeyID, sessionKey)
	if err != nil {
		return err
	}
	if err := s.client.Expire(ctx, key, OpenAIBPSSessionTTL).Err(); err != nil {
		return fmt.Errorf("openai bps session store: touch: %w", err)
	}
	return nil
}
