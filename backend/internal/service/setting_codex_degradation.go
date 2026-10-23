package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	// SettingKeyCodexDegradation 是自动降智检测设置的存储键。
	SettingKeyCodexDegradation = "openai_codex_degradation"
	// SettingKeyCodexProbeTemplate 是可编辑 JSONL 探测模板的存储键。
	// 空值表示使用内嵌默认模板（codex_probe_template.jsonl）。
	SettingKeyCodexProbeTemplate = "openai_codex_probe_template"
)

// GetCodexDegradationSettings 读取运行器配置；key 缺失时返回默认值
// （enabled=false），字段缺失/非法时逐项回退默认，保证运行器永远拿到可用配置。
func (s *SettingService) GetCodexDegradationSettings(ctx context.Context) (*CodexDegradationSettings, error) {
	defaults := DefaultCodexDegradationSettings()
	if s == nil || s.settingRepo == nil {
		return &defaults, nil
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyCodexDegradation)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return &defaults, nil
		}
		return nil, fmt.Errorf("get codex degradation settings: %w", err)
	}
	if strings.TrimSpace(value) == "" {
		return &defaults, nil
	}
	settings := defaults
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		// 损坏的配置绝不阻塞运行器：回退默认（关闭）并保持服务可用。
		return &defaults, nil
	}
	normalizeCodexDegradationSettings(&settings)
	return &settings, nil
}

// SetCodexDegradationSettings 校验并保存运行器配置；校验失败不落库。
func (s *SettingService) SetCodexDegradationSettings(ctx context.Context, settings *CodexDegradationSettings) error {
	if s == nil || s.settingRepo == nil {
		return errors.New("setting repository unavailable")
	}
	if err := ValidateCodexDegradationSettings(settings); err != nil {
		return err
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal codex degradation settings: %w", err)
	}
	return s.settingRepo.Set(ctx, SettingKeyCodexDegradation, string(raw))
}

// GetCodexProbeTemplateRaw 读取 JSONL 探测模板原文；key 缺失时返回内嵌默认模板。
func (s *SettingService) GetCodexProbeTemplateRaw(ctx context.Context) (string, error) {
	if s == nil || s.settingRepo == nil {
		return DefaultCodexProbeTemplate(), nil
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyCodexProbeTemplate)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return DefaultCodexProbeTemplate(), nil
		}
		return "", fmt.Errorf("get codex probe template: %w", err)
	}
	if strings.TrimSpace(value) == "" {
		return DefaultCodexProbeTemplate(), nil
	}
	return value, nil
}

// GetCodexProbeTemplate 读取并校验 JSONL 探测模板。模板非法时返回
// ErrCodexProbeTemplateInvalid，调用方必须拒绝发出探测请求。
func (s *SettingService) GetCodexProbeTemplate(ctx context.Context) (*CodexProbeTemplate, error) {
	if s == nil || s.settingRepo == nil {
		return parsedDefaultCodexProbeTemplate()
	}
	raw, err := s.GetCodexProbeTemplateRaw(ctx)
	if err != nil {
		return nil, err
	}
	return ParseCodexProbeTemplate(raw)
}

// SetCodexProbeTemplate 校验并保存 JSONL 探测模板；非法模板拒绝保存。
// 传空字符串等价于恢复内嵌默认模板。
func (s *SettingService) SetCodexProbeTemplate(ctx context.Context, raw string) error {
	if s == nil || s.settingRepo == nil {
		return errors.New("setting repository unavailable")
	}
	if strings.TrimSpace(raw) == "" {
		return s.settingRepo.Set(ctx, SettingKeyCodexProbeTemplate, "")
	}
	if _, err := ParseCodexProbeTemplate(raw); err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, SettingKeyCodexProbeTemplate, raw)
}
