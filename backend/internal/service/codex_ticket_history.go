package service

import (
	"context"
	"time"
)

// 本文件是票据管理面查询：打票流水、事件时间线、失效流水、参与开关、节奏读取。
// 全部走仓储窄接口；仓储未接线时返回 ErrCodexTicketUnavailable，不影响转发主链路。

// History 返回账号的打票流水（不分成功/失败），page 从 1 开始。
func (s *CodexTicketService) History(ctx context.Context, accountID int64, model string, page, size int) ([]CodexTicketAttempt, int64, error) {
	return s.HistoryFiltered(ctx, accountID, model, false, page, size)
}

// HistoryFiltered 返回账号的打票流水，successOnly 只保留成功记录。
func (s *CodexTicketService) HistoryFiltered(ctx context.Context, accountID int64, model string, successOnly bool, page, size int) ([]CodexTicketAttempt, int64, error) {
	if s == nil || s.attempts == nil {
		return nil, 0, ErrCodexTicketUnavailable
	}
	account, err := s.requireTicketAccount(ctx, accountID)
	if err != nil {
		return nil, 0, err
	}
	_ = account
	model = normalizeCodexTicketModel(model)
	if model != "" && !s.gatedModel(model) {
		return nil, 0, ErrCodexTicketModel
	}
	page, size = normalizeCodexTicketPage(page, size)
	return s.attempts.List(ctx, accountID, model, successOnly, page, size)
}

// Events 返回 attempt + invalidation 合并后的时间线，filter 取 all/attempts/success/failure/invalidation。
func (s *CodexTicketService) Events(ctx context.Context, accountID int64, model, filter string, start, end time.Time, page, size int) ([]CodexTicketEvent, int64, error) {
	if s == nil || s.events == nil {
		return nil, 0, ErrCodexTicketUnavailable
	}
	if _, err := s.requireTicketAccount(ctx, accountID); err != nil {
		return nil, 0, err
	}
	model = normalizeCodexTicketModel(model)
	if model != "" && !s.gatedModel(model) {
		return nil, 0, ErrCodexTicketModel
	}
	switch filter {
	case "", "all":
		filter = "all"
	case "attempts", "success", "failure", "invalidation":
	default:
		return nil, 0, ErrCodexTicketModel
	}
	page, size = normalizeCodexTicketPage(page, size)
	return s.events.ListEvents(ctx, accountID, model, filter, start, end, page, size)
}

// LatestEvents 批量返回账号最近一次打票/失效事件（账号列表页使用）。
func (s *CodexTicketService) LatestEvents(ctx context.Context, accountIDs []int64) (map[int64]CodexTicketRecentEvent, error) {
	if s == nil || s.recent == nil {
		return map[int64]CodexTicketRecentEvent{}, nil
	}
	return s.recent.LatestEvents(ctx, accountIDs)
}

// ListInvalidations 返回账号的票据失效流水（列表，不含票据本体）。
func (s *CodexTicketService) ListInvalidations(ctx context.Context, accountID int64, model string, start, end time.Time, page, size int) ([]CodexTicketInvalidationSummary, int64, error) {
	if s == nil || s.lifecycle == nil {
		return nil, 0, ErrCodexTicketUnavailable
	}
	if _, err := s.requireTicketAccount(ctx, accountID); err != nil {
		return nil, 0, err
	}
	model = normalizeCodexTicketModel(model)
	if model != "" && !s.gatedModel(model) {
		return nil, 0, ErrCodexTicketModel
	}
	page, size = normalizeCodexTicketPage(page, size)
	return s.lifecycle.ListInvalidations(ctx, accountID, model, start, end, page, size)
}

// GetInvalidation 返回单条失效流水详情（含脱敏前的票据原文，仅管理端诊断用）。
func (s *CodexTicketService) GetInvalidation(ctx context.Context, accountID, eventID int64) (*CodexTicketInvalidation, error) {
	if s == nil || s.lifecycle == nil {
		return nil, ErrCodexTicketUnavailable
	}
	if _, err := s.requireTicketAccount(ctx, accountID); err != nil {
		return nil, err
	}
	return s.lifecycle.GetInvalidation(ctx, accountID, eventID)
}

// SetParticipation 保存账号级打票参与开关与逐模型开关。
// 退出参与时不清除已捕获票据（票据仍然可用于注入，只是不再主动打票）。
func (s *CodexTicketService) SetParticipation(ctx context.Context, accountID int64, enabled bool, models map[string]bool) error {
	if s == nil || s.accountRepo == nil {
		return ErrCodexTicketUnavailable
	}
	account, err := s.requireTicketAccount(ctx, accountID)
	if err != nil {
		return err
	}
	for model := range models {
		if !s.gatedModel(model) {
			return ErrCodexTicketModel
		}
	}
	values := make(map[string]any, len(models))
	for model, participating := range models {
		values[normalizeCodexTicketModel(model)] = participating
	}
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
		codexTicketAccountEnabledKey: enabled,
		codexTicketModelsEnabledKey:  values,
	}); err != nil {
		return err
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	account.Extra[codexTicketAccountEnabledKey] = enabled
	account.Extra[codexTicketModelsEnabledKey] = values
	if enabled {
		s.notifyHarvester()
	}
	return nil
}

// Cadence 返回当前打票节奏（设置面 + 静态配置兜底）。
func (s *CodexTicketService) Cadence(ctx context.Context) CodexTicketCadence {
	if s == nil {
		return CodexTicketCadence{}
	}
	return s.runtimeSettings(ctx).cadence
}

// ProxyPool 返回当前打票代理池设置。
func (s *CodexTicketService) ProxyPool(ctx context.Context) (CodexTicketProxyPool, error) {
	if s == nil || s.settingSvc == nil {
		return CodexTicketProxyPool{}, ErrCodexTicketUnavailable
	}
	return s.settingSvc.GetCodexTicketPool(ctx)
}

// SetProxyPool 保存打票代理池设置并触发一轮扫描。
func (s *CodexTicketService) SetProxyPool(ctx context.Context, pool CodexTicketProxyPool) error {
	if s == nil || s.settingSvc == nil {
		return ErrCodexTicketUnavailable
	}
	if err := s.settingSvc.SetCodexTicketPool(ctx, pool); err != nil {
		return err
	}
	_ = s.loadSettings(ctx)
	s.notifyHarvester()
	return nil
}

// SetCadence 保存打票节奏并刷新设置快照。
func (s *CodexTicketService) SetCadence(ctx context.Context, cadence CodexTicketCadence) error {
	if s == nil || s.settingSvc == nil {
		return ErrCodexTicketUnavailable
	}
	if err := s.settingSvc.SetCodexTicketCadence(ctx, cadence); err != nil {
		return err
	}
	_ = s.loadSettings(ctx)
	return nil
}

// SetEnabled 保存全局打票开关并刷新设置快照。
func (s *CodexTicketService) SetEnabled(ctx context.Context, enabled bool) error {
	if s == nil || s.settingSvc == nil {
		return ErrCodexTicketUnavailable
	}
	if err := s.settingSvc.SetCodexTicketEnabled(ctx, enabled); err != nil {
		return err
	}
	_ = s.loadSettings(ctx)
	if enabled {
		s.notifyHarvester()
	}
	return nil
}

// SetAllowWithoutTicket 保存「无票是否放行」。
func (s *CodexTicketService) SetAllowWithoutTicket(ctx context.Context, allow bool) error {
	if s == nil || s.settingSvc == nil {
		return ErrCodexTicketUnavailable
	}
	if err := s.settingSvc.SetCodexTicketAllowWithoutTicket(ctx, allow); err != nil {
		return err
	}
	_ = s.loadSettings(ctx)
	return nil
}

// AllowWithoutTicket 返回当前全局「无票放行」设置。
func (s *CodexTicketService) AllowWithoutTicket(ctx context.Context) bool {
	return s.runtimeSettings(ctx).allowWithoutTicket
}

// requireTicketAccount 读取账号并校验其参与打票。
func (s *CodexTicketService) requireTicketAccount(ctx context.Context, accountID int64) (*Account, error) {
	if s == nil || s.accountRepo == nil || accountID <= 0 {
		return nil, ErrCodexTicketUnavailable
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !isCodexTicketAccount(account) {
		return nil, ErrCodexTicketUnavailable
	}
	return account, nil
}

// normalizeCodexTicketPage 归一化分页参数（管理端列表统一上限 100）。
func normalizeCodexTicketPage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return page, size
}
