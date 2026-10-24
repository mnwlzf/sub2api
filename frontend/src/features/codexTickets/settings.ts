/**
 * Codex 打票全局设置的纯函数（校验 / 代理池载荷 / 摘要）。
 *
 * 后端可编辑项（/admin/codex-tickets/*）：
 *   - enabled、allow_without_ticket
 *   - cadence：retry_min_seconds / retry_max_seconds / refresh_seconds
 *   - pool：mode(all|custom) + proxy_ids
 * target_length / ttl_seconds / refresh_before_seconds / harvest_* 属于
 * gateway.openai_codex_ticket.* 配置文件项，管理端没有写入接口。
 */

import type { CodexTicketCadence, CodexTicketProxyPool } from '@/api/admin/codexTicket'

export type TicketCadenceError =
  | 'retryMinPositive'
  | 'retryMaxPositive'
  | 'refreshPositive'
  | 'retryMinGreaterThanMax'

/** 后端约束：cadence 各值 ≥1，retry min ≤ max（max 上限 3600 / refresh 上限 86400 仅提示）。 */
export function validateTicketCadence(cadence: CodexTicketCadence): TicketCadenceError | null {
  const { retry_min_seconds: min, retry_max_seconds: max, refresh_seconds: refresh } = cadence
  if (!Number.isFinite(min) || min < 1) return 'retryMinPositive'
  if (!Number.isFinite(max) || max < 1) return 'retryMaxPositive'
  if (!Number.isFinite(refresh) || refresh < 1) return 'refreshPositive'
  if (min > max) return 'retryMinGreaterThanMax'
  return null
}

/** 统一成整数，避免 input 数字框产生小数被后端拒绝。 */
export function normalizeTicketCadence(cadence: CodexTicketCadence): CodexTicketCadence {
  return {
    retry_min_seconds: Math.max(1, Math.trunc(Number(cadence.retry_min_seconds) || 1)),
    retry_max_seconds: Math.max(1, Math.trunc(Number(cadence.retry_max_seconds) || 1)),
    refresh_seconds: Math.max(1, Math.trunc(Number(cadence.refresh_seconds) || 1))
  }
}

/**
 * 构造代理池保存载荷：
 *   - all：后端会清空 proxy_ids，这里也传空数组；
 *   - custom：去重、升序、过滤非正整数。
 */
export function buildTicketProxyPoolPayload(
  mode: CodexTicketProxyPool['mode'],
  proxyIds: number[]
): CodexTicketProxyPool {
  if (mode === 'all') return { mode: 'all', proxy_ids: [] }
  const unique = Array.from(
    new Set(proxyIds.filter(id => Number.isInteger(id) && id > 0))
  ).sort((a, b) => a - b)
  return { mode: 'custom', proxy_ids: unique }
}

/** 自定义池模式下是否缺少代理（后端会拒绝 custom + 空列表）。 */
export function ticketProxyPoolError(pool: CodexTicketProxyPool): 'noProxySelected' | null {
  if (pool.mode === 'custom' && pool.proxy_ids.length === 0) return 'noProxySelected'
  return null
}

/** 打票模型展示：后端打票模型来自配置，管理端只在账号参与开关里出现。 */
export function describeTicketCadence(cadence: CodexTicketCadence): string {
  return `retry ${cadence.retry_min_seconds}-${cadence.retry_max_seconds}s · refresh ${cadence.refresh_seconds}s`
}
