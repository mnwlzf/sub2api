/**
 * Codex 打票状态展示的纯函数。
 *
 * 后端 GET /admin/accounts/:id/codex-tickets 返回逐模型的票据摘要；账号列表列与
 * 票据中心弹窗共用这里的「状态归类 / 剩余时长格式化 / 参与开关」逻辑，
 * 保证同一份数据在列表与弹窗里展示一致，并且可以脱离组件单测。
 */

import type { CodexTicketStatus, CodexTicketStatusesResponse } from '@/api/admin/codexTicket'

/** 列表/弹窗统一展示的票据状态。 */
export type CodexTicketDisplayState =
  | 'ready'
  | 'expiring'
  | 'expired'
  | 'missing'
  | 'blocked'
  | 'paused'
  | 'disabled'
  | 'not_participating'
  | 'unavailable'

/** 临期阈值：与后端默认 refresh_before_seconds=600 对齐，仅用于展示「临期」。 */
export const CODEX_TICKET_EXPIRING_SECONDS = 600

/** 打票账号判定：OpenAI OAuth / SetupToken 且不是影子账号（与后端 isCodexTicketAccount 一致）。 */
export function isCodexTicketEligibleAccount(account: {
  platform?: string | null
  type?: string | null
  parent_account_id?: number | null
} | null | undefined): boolean {
  if (!account) return false
  if (account.platform !== 'openai') return false
  if (account.type !== 'oauth' && account.type !== 'setup-token') return false
  return account.parent_account_id == null
}

/** 过滤掉非 Codex 账号返回的空 items 与空 model 项。 */
export function normalizeTicketStatuses(
  response: CodexTicketStatusesResponse | null | undefined
): CodexTicketStatus[] {
  const items = response?.items
  if (!Array.isArray(items)) return []
  return items.filter(
    (item): item is CodexTicketStatus =>
      !!item && typeof item.model === 'string' && item.model.trim() !== ''
  )
}

/**
 * 把一个模型的状态摘要归到单一展示态。
 * 顺序很重要：未参与 > 全局/账号未启用 > 阻塞 > 限流暂停 > 可用/临期 > 过期 > 缺失。
 */
export function classifyTicketStatus(status: CodexTicketStatus): CodexTicketDisplayState {
  if (!status.participation_enabled) return 'not_participating'
  if (!status.harvest_enabled) return 'disabled'
  if (status.blocked) return 'blocked'
  if (status.harvest_paused) return 'paused'
  if (status.ready) {
    return status.remaining_seconds > 0 && status.remaining_seconds <= CODEX_TICKET_EXPIRING_SECONDS
      ? 'expiring'
      : 'ready'
  }
  if (status.captured_at || status.expires_at) return 'expired'
  return 'missing'
}

/** 展示态对应的 i18n key 后缀（与 admin.codex.ticket.state.* 对齐）。 */
export function ticketStateI18nKey(state: CodexTicketDisplayState): string {
  return `admin.codex.ticket.state.${state}`
}

/** 把秒数格式化成紧凑时长（用于票据列与弹窗）。 */
export function formatTicketRemainingSeconds(seconds: number | null | undefined): string {
  const value = Number(seconds)
  if (!Number.isFinite(value) || value <= 0) return '0s'
  const total = Math.floor(value)
  if (total < 60) return `${total}s`
  const minutes = Math.floor(total / 60)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  const restMinutes = minutes % 60
  if (hours < 24) return restMinutes > 0 ? `${hours}h ${restMinutes}m` : `${hours}h`
  const days = Math.floor(hours / 24)
  const restHours = hours % 24
  return restHours > 0 ? `${days}d ${restHours}h` : `${days}d`
}

export interface CodexTicketStatusCounts {
  total: number
  ready: number
  expiring: number
  expired: number
  missing: number
  blocked: number
  paused: number
  disabled: number
  notParticipating: number
  /** 所有模型都就绪（含临期）。 */
  allReady: boolean
  /** 至少一个模型阻塞/缺失/过期。 */
  hasProblem: boolean
}

/** 汇总一组状态，供列级提示与弹窗顶部 badge 使用。 */
export function countTicketStates(statuses: CodexTicketStatus[]): CodexTicketStatusCounts {
  const counts: CodexTicketStatusCounts = {
    total: statuses.length,
    ready: 0,
    expiring: 0,
    expired: 0,
    missing: 0,
    blocked: 0,
    paused: 0,
    disabled: 0,
    notParticipating: 0,
    allReady: false,
    hasProblem: false
  }
  for (const status of statuses) {
    const state = classifyTicketStatus(status)
    switch (state) {
      case 'ready':
        counts.ready += 1
        break
      case 'expiring':
        counts.expiring += 1
        break
      case 'expired':
        counts.expired += 1
        break
      case 'missing':
        counts.missing += 1
        break
      case 'blocked':
        counts.blocked += 1
        break
      case 'paused':
        counts.paused += 1
        break
      case 'disabled':
        counts.disabled += 1
        break
      case 'not_participating':
        counts.notParticipating += 1
        break
      default:
        break
    }
  }
  counts.allReady = counts.total > 0 && counts.ready + counts.expiring === counts.total
  counts.hasProblem = counts.expired + counts.missing + counts.blocked > 0
  return counts
}

/** 列级一句话摘要的 i18n key（无数据时返回 null）。 */
export function ticketSummaryI18nKey(
  counts: CodexTicketStatusCounts
): string | null {
  if (counts.total === 0) return null
  if (counts.allReady) return 'admin.codex.ticket.summary.allReady'
  if (counts.blocked > 0) return 'admin.codex.ticket.summary.blocked'
  if (counts.missing + counts.expired > 0) return 'admin.codex.ticket.summary.missing'
  if (counts.disabled === counts.total) return 'admin.codex.ticket.summary.disabled'
  if (counts.notParticipating === counts.total) return 'admin.codex.ticket.summary.notParticipating'
  return 'admin.codex.ticket.summary.partial'
}

/** 票据长度是否符合账号规则（team 332 / 其余 292），用于弹窗诊断展示。 */
export function isTicketLengthExpected(status: Pick<CodexTicketStatus, 'length' | 'expected_ticket_length'>): boolean {
  if (!status.expected_ticket_length) return true
  return status.length === status.expected_ticket_length
}

/** 参与开关的本地状态。 */
export interface CodexTicketParticipation {
  enabled: boolean
  models: Record<string, boolean>
}

/** 从状态列表 + 账号级 extra 初始化参与开关。 */
export function buildParticipationFromStatuses(
  statuses: CodexTicketStatus[],
  accountEnabled: boolean
): CodexTicketParticipation {
  const models: Record<string, boolean> = {}
  for (const status of statuses) {
    models[status.model] = status.participation_enabled
  }
  return { enabled: accountEnabled, models }
}

/** 从账号 extra 读取账号级参与开关（缺省=参与）。 */
export function readAccountTicketParticipationEnabled(
  extra: Record<string, unknown> | null | undefined
): boolean {
  if (!extra) return true
  return extra.codex_ticket_harvest_enabled !== false
}

/** 从账号 extra 读取逐模型开关（缺省=参与）。 */
export function readAccountTicketParticipationModels(
  extra: Record<string, unknown> | null | undefined
): Record<string, boolean> {
  const raw = extra?.codex_ticket_harvest_models
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return {}
  const result: Record<string, boolean> = {}
  for (const [model, value] of Object.entries(raw as Record<string, unknown>)) {
    if (typeof value === 'boolean') result[model] = value
  }
  return result
}

/** 手动打票按钮的禁用原因（空串表示可用）。 */
export function manualHarvestBlockedReason(
  response: Pick<CodexTicketStatusesResponse, 'manual_available' | 'manual_unavailable_reason'> | null
): string {
  if (!response) return 'unavailable'
  if (response.manual_available) return ''
  return response.manual_unavailable_reason || 'unavailable'
}
