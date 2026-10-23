package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 本文件是 Codex 打票功能的「设置面」：所有方法都追加在既有 SettingService 上
// （同包新文件，不修改 setting_service.go），复用其 settingRepo / proxyRepo。
//
// 设置键沿用参考实现（sub4api）的命名，方便存量部署迁移：
//   - openai_codex_ticket_enabled：全局总开关（默认关闭）
//   - openai_codex_ticket_allow_without_ticket：无票是否放行（默认放行）
//   - openai_codex_ticket_proxy_pool：打票代理池（all / custom + proxy_ids）
//   - openai_codex_ticket_cadence：失败退避与主动刷新节奏
const (
	CodexTicketSettingEnabled            = "openai_codex_ticket_enabled"
	CodexTicketSettingAllowWithoutTicket = "openai_codex_ticket_allow_without_ticket"
	CodexTicketSettingProxyPool          = "openai_codex_ticket_proxy_pool"
	CodexTicketSettingCadence            = "openai_codex_ticket_cadence"

	CodexTicketPoolModeAll    = "all"
	CodexTicketPoolModeCustom = "custom"

	codexTicketCadenceMinSeconds = 1
	codexTicketCadenceMaxRetry   = 3600
	codexTicketCadenceMaxRefresh = 86400
)

// GetCodexTicketEnabled 读取全局打票开关；设置不存在或读取失败时回退 fallback。
// 该方法是「读」路径，调用方（CodexTicketService）自己负责缓存，避免热路径打 DB。
func (s *SettingService) GetCodexTicketEnabled(ctx context.Context, fallback bool) bool {
	return s.getCodexTicketBoolSetting(ctx, CodexTicketSettingEnabled, fallback)
}

// SetCodexTicketEnabled 写入全局打票开关。
func (s *SettingService) SetCodexTicketEnabled(ctx context.Context, enabled bool) error {
	if s == nil || s.settingRepo == nil {
		return ErrCodexTicketUnavailable
	}
	return s.settingRepo.Set(ctx, CodexTicketSettingEnabled, strconv.FormatBool(enabled))
}

// GetCodexTicketAllowWithoutTicket 读取「无票是否放行」；默认放行。
func (s *SettingService) GetCodexTicketAllowWithoutTicket(ctx context.Context, fallback bool) bool {
	return s.getCodexTicketBoolSetting(ctx, CodexTicketSettingAllowWithoutTicket, fallback)
}

// SetCodexTicketAllowWithoutTicket 写入「无票是否放行」。
func (s *SettingService) SetCodexTicketAllowWithoutTicket(ctx context.Context, allow bool) error {
	if s == nil || s.settingRepo == nil {
		return ErrCodexTicketUnavailable
	}
	return s.settingRepo.Set(ctx, CodexTicketSettingAllowWithoutTicket, strconv.FormatBool(allow))
}

func (s *SettingService) getCodexTicketBoolSetting(ctx context.Context, key string, fallback bool) bool {
	if s == nil || s.settingRepo == nil {
		return fallback
	}
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := s.settingRepo.GetValue(ctx, key)
	if err != nil {
		return fallback
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return value
}

// GetCodexTicketPool 读取打票代理池；未配置时默认 all（全部未过期代理）。
func (s *SettingService) GetCodexTicketPool(ctx context.Context) (CodexTicketProxyPool, error) {
	pool := CodexTicketProxyPool{Mode: CodexTicketPoolModeAll, ProxyIDs: []int64{}}
	if s == nil || s.settingRepo == nil {
		return pool, ErrCodexTicketUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := s.settingRepo.GetValue(ctx, CodexTicketSettingProxyPool)
	if errors.Is(err, ErrSettingNotFound) {
		return pool, nil
	}
	if err != nil {
		return pool, err
	}
	if err := json.Unmarshal([]byte(raw), &pool); err != nil {
		return CodexTicketProxyPool{Mode: CodexTicketPoolModeAll, ProxyIDs: []int64{}}, fmt.Errorf("decode codex ticket proxy pool: %w", err)
	}
	if pool.Mode != CodexTicketPoolModeAll && pool.Mode != CodexTicketPoolModeCustom {
		return CodexTicketProxyPool{Mode: CodexTicketPoolModeAll, ProxyIDs: []int64{}}, errors.New("invalid codex ticket proxy pool mode")
	}
	if pool.ProxyIDs == nil {
		pool.ProxyIDs = []int64{}
	}
	return pool, nil
}

// SetCodexTicketPool 校验并写入打票代理池。
func (s *SettingService) SetCodexTicketPool(ctx context.Context, pool CodexTicketProxyPool) error {
	if s == nil || s.settingRepo == nil || s.proxyRepo == nil {
		return ErrCodexTicketUnavailable
	}
	if pool.Mode != CodexTicketPoolModeAll && pool.Mode != CodexTicketPoolModeCustom {
		return errors.New("mode must be all or custom")
	}
	if pool.Mode == CodexTicketPoolModeAll {
		pool.ProxyIDs = []int64{}
	}
	if pool.Mode == CodexTicketPoolModeCustom {
		if len(pool.ProxyIDs) == 0 {
			return errors.New("select at least one proxy")
		}
		seen := make(map[int64]bool, len(pool.ProxyIDs))
		for _, id := range pool.ProxyIDs {
			if id <= 0 || seen[id] {
				return errors.New("invalid or duplicate proxy ID")
			}
			seen[id] = true
		}
		found, err := s.proxyRepo.ListByIDs(ctx, pool.ProxyIDs)
		if err != nil {
			return err
		}
		if len(found) != len(pool.ProxyIDs) {
			return errors.New("proxy not found")
		}
		sort.Slice(pool.ProxyIDs, func(i, j int) bool { return pool.ProxyIDs[i] < pool.ProxyIDs[j] })
	}
	data, err := json.Marshal(pool)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, CodexTicketSettingProxyPool, string(data))
}

// AvailableCodexTicketProxies 返回当前可参与打票轮换的代理（active 且未过期）。
// 复用本地「IP 设置」里的代理记录，不引入第二套代理配置。
func (s *SettingService) AvailableCodexTicketProxies(ctx context.Context) ([]Proxy, error) {
	if s == nil || s.proxyRepo == nil {
		return nil, ErrCodexTicketUnavailable
	}
	pool, err := s.GetCodexTicketPool(ctx)
	if err != nil {
		return nil, err
	}
	proxies, err := s.proxyRepo.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	selected := make(map[int64]bool, len(pool.ProxyIDs))
	for _, id := range pool.ProxyIDs {
		selected[id] = true
	}
	now := time.Now()
	available := make([]Proxy, 0, len(proxies))
	for _, proxy := range proxies {
		if proxy.IsExpired(now) {
			continue
		}
		if pool.Mode == CodexTicketPoolModeCustom && !selected[proxy.ID] {
			continue
		}
		available = append(available, proxy)
	}
	sort.Slice(available, func(i, j int) bool { return available[i].ID < available[j].ID })
	return available, nil
}

// GetCodexTicketCadence 读取打票节奏；未配置/非法时回退 fallback。
func (s *SettingService) GetCodexTicketCadence(ctx context.Context, fallback CodexTicketCadence) CodexTicketCadence {
	if s == nil || s.settingRepo == nil {
		return fallback
	}
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := s.settingRepo.GetValue(ctx, CodexTicketSettingCadence)
	if err != nil {
		return fallback
	}
	var value CodexTicketCadence
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return fallback
	}
	if err := ValidateCodexTicketCadence(value); err != nil {
		return fallback
	}
	return value
}

// SetCodexTicketCadence 校验并写入打票节奏。
func (s *SettingService) SetCodexTicketCadence(ctx context.Context, value CodexTicketCadence) error {
	if s == nil || s.settingRepo == nil {
		return ErrCodexTicketUnavailable
	}
	if err := ValidateCodexTicketCadence(value); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, CodexTicketSettingCadence, string(data))
}

// ValidateCodexTicketCadence 校验节奏取值范围。
func ValidateCodexTicketCadence(value CodexTicketCadence) error {
	if value.RetryMinSeconds < codexTicketCadenceMinSeconds || value.RetryMinSeconds > codexTicketCadenceMaxRetry ||
		value.RetryMaxSeconds < value.RetryMinSeconds || value.RetryMaxSeconds > codexTicketCadenceMaxRetry ||
		value.RefreshSeconds < 0 || value.RefreshSeconds > codexTicketCadenceMaxRefresh {
		return errors.New("retry interval must be 1..3600 seconds with max >= min, refresh interval 0..86400 seconds")
	}
	return nil
}
