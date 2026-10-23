/**
 * 自动降智检测设置与探测模板的纯函数（校验 / 归一化）。
 */

import type { CodexDegradationSettings } from '@/api/admin/codexDegradation'

export type DegradationSettingsError =
  | 'intervalPositive'
  | 'blockPositive'
  | 'modelsRequired'
  | 'modelsNotAvailable'

/**
 * 校验自动检测设置：interval/block ≥ 1，models 非空、无重复且都在可选模型内。
 * 与后端 ValidateCodexDegradationSettings 保持同样的拒绝条件。
 */
export function validateDegradationSettings(
  settings: CodexDegradationSettings,
  availableModels: string[]
): DegradationSettingsError | null {
  if (!Number.isFinite(settings.interval_minutes) || settings.interval_minutes < 1) {
    return 'intervalPositive'
  }
  if (!Number.isFinite(settings.block_minutes) || settings.block_minutes < 1) {
    return 'blockPositive'
  }
  const models = settings.models ?? []
  if (models.length === 0) return 'modelsRequired'
  const available = new Set(availableModels)
  for (const model of models) {
    if (!available.has(model)) return 'modelsNotAvailable'
  }
  return null
}

/** 保存前归一化：整数化间隔/封锁时长，模型去重并保留顺序。 */
export function normalizeDegradationSettingsPayload(
  settings: CodexDegradationSettings
): CodexDegradationSettings {
  const seen = new Set<string>()
  const models: string[] = []
  for (const model of settings.models ?? []) {
    const value = typeof model === 'string' ? model.trim() : ''
    if (!value || seen.has(value)) continue
    seen.add(value)
    models.push(value)
  }
  return {
    enabled: settings.enabled === true,
    interval_minutes: Math.max(1, Math.trunc(Number(settings.interval_minutes) || 1)),
    block_minutes: Math.max(1, Math.trunc(Number(settings.block_minutes) || 1)),
    models
  }
}

/**
 * 判断探测模板编辑框内容是否等于默认模板。
 * 保存空串等价于「清空覆盖、使用内嵌默认模板」，恢复默认按钮据此走清空路径。
 */
export function isDefaultProbeTemplate(content: string, defaultTemplate: string): boolean {
  return content.trim() === defaultTemplate.trim()
}

/** 保存探测模板时去掉首尾空白（空串表示恢复内嵌默认）。 */
export function normalizeProbeTemplate(content: string): string {
  return content.trim()
}
