/**
 * Codex 降智检测结果展示的纯函数。
 *
 * 后端四态：normal / degraded / uncertain / failed。UI 需要在弹窗结果卡片、
 * 历史列表与设置页里保持一致的颜色与文案映射，这里集中定义以便单测。
 */

import type {
  CodexDegradationCheckResult,
  CodexDegradationProbeResult,
  CodexDegradationStatus
} from '@/api/admin/codexDegradation'

export const CODEX_DEGRADATION_STATUSES: readonly CodexDegradationStatus[] = [
  'normal',
  'degraded',
  'uncertain',
  'failed'
] as const

/** 收窄任意字符串到四态；未知值按 failed 处理（与后端「不可评分即失败」一致）。 */
export function normalizeDegradationStatus(value: unknown): CodexDegradationStatus {
  return typeof value === 'string' && (CODEX_DEGRADATION_STATUSES as readonly string[]).includes(value)
    ? (value as CodexDegradationStatus)
    : 'failed'
}

export function degradationStatusI18nKey(status: CodexDegradationStatus): string {
  return `admin.codex.degradation.status.${status}`
}

export function degradationStatusBadgeClass(status: CodexDegradationStatus): string {
  switch (status) {
    case 'normal':
      return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
    case 'degraded':
      return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
    case 'uncertain':
      return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
    default:
      return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
  }
}

export function degradationStatusDotClass(status: CodexDegradationStatus): string {
  switch (status) {
    case 'normal':
      return 'bg-emerald-500'
    case 'degraded':
      return 'bg-red-500'
    case 'uncertain':
      return 'bg-amber-500'
    default:
      return 'bg-gray-400'
  }
}

/** 概率展示：后端返回 0..1，未知返回 '-'。 */
export function formatDegradationProbability(probability: number | null | undefined): string {
  if (typeof probability !== 'number' || !Number.isFinite(probability)) return '-'
  return `${(probability * 100).toFixed(1)}%`
}

export interface DegradationStatusCounts {
  normal: number
  degraded: number
  uncertain: number
  failed: number
  total: number
}

/** 统计一次检测结果里的四态数量。 */
export function countDegradationStatuses(
  items: CodexDegradationProbeResult[] | null | undefined
): DegradationStatusCounts {
  const counts: DegradationStatusCounts = {
    normal: 0,
    degraded: 0,
    uncertain: 0,
    failed: 0,
    total: 0
  }
  for (const item of items ?? []) {
    counts[normalizeDegradationStatus(item.status)] += 1
    counts.total += 1
  }
  return counts
}

/** 整次检测的展示摘要 key。 */
export function degradationSummaryI18nKey(
  result: Pick<CodexDegradationCheckResult, 'status'> | null | undefined
): string {
  return degradationStatusI18nKey(normalizeDegradationStatus(result?.status))
}

/** 检测结果是否值得提示「有模型降智」。 */
export function hasDegradedModels(
  result: Pick<CodexDegradationCheckResult, 'items'> | null | undefined
): boolean {
  return (result?.items ?? []).some(item => normalizeDegradationStatus(item.status) === 'degraded')
}

/** 从设置模型与可选模型中挑选预选模型：优先设置值，其次第一个可用模型。 */
export function resolveInitialDiagnosticModels(
  settingsModels: string[] | null | undefined,
  availableModels: string[] | null | undefined
): string[] {
  const available = (availableModels ?? []).filter(model => typeof model === 'string' && model.trim() !== '')
  if (available.length === 0) return []
  const selected = (settingsModels ?? []).filter(model => available.includes(model))
  return selected.length > 0 ? selected : [available[0]]
}
