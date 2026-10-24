/**
 * Admin Codex Ticket (打票) API endpoints
 *
 * 契约来源：backend/internal/handler/admin/codex_ticket_handler.go
 * 与 backend/internal/service/codex_ticket_types.go。
 * 所有响应均为管理端脱敏视图：票据本体 / Cookie / 代理 URL 不会出现在这里。
 */

import { apiClient } from '../client'
import type { Proxy } from '@/types'

/** 单个模型上的当前票据摘要（绝不含票据本体）。 */
export interface CodexTicketStatus {
  model: string
  length: number
  captured_at?: string
  expires_at?: string
  turn_state_present: boolean
  cookie_present: boolean
  ready: boolean
  blocked: boolean
  reusing_expired?: boolean
  remaining_seconds: number
  expected_ticket_length: number
  harvest_enabled: boolean
  harvest_paused: boolean
  participation_enabled: boolean
  quota_reset_at?: string
  harvest_resume_at?: string
  reason?: string
}

/** GET /admin/accounts/:id/codex-tickets */
export interface CodexTicketStatusesResponse {
  account_id: number
  /** 非 Codex 打票账号（非 OpenAI OAuth / SetupToken / 影子账号）时为 null。 */
  items: CodexTicketStatus[] | null
  manual_available: boolean
  manual_unavailable_reason: string
}

export type CodexTicketAttemptOutcome = 'success' | 'miss' | 'error'
export type CodexTicketAttemptTrigger = string

/** 一条打票流水（仅元数据）。 */
export interface CodexTicketAttempt {
  id: number
  account_id: number
  model: string
  occurred_at: string
  outcome: CodexTicketAttemptOutcome
  trigger: CodexTicketAttemptTrigger
  http_status?: number
  ticket_length?: number
  duration_ms: number
  reason_code?: string
  proxy_id?: number
  proxy_name?: string
  expires_at?: string
  ticket_generation_id?: string
  verification_method?: string
  turn_state_present?: boolean
  cookie_present?: boolean
  challenge_expected_count?: number
  parsed_number_count?: number
}

/** GET /admin/accounts/:id/codex-ticket-history */
export interface CodexTicketHistoryResponse {
  items: CodexTicketAttempt[] | null
  total: number
  page: number
  page_size: number
  /** 仅在传了 model 查询参数时由后端填充；否则为全零结构。 */
  ticket_status: CodexTicketStatus
}

export type CodexTicketEventKind = 'success' | 'miss' | 'error' | 'invalidation'

/**
 * 时间线事件：后端把 attempt / invalidation 两种行拼成同一个 map。
 * 保留索引签名以兼容后端后续新增字段，同时给已知字段提供类型。
 */
export interface CodexTicketEvent {
  id?: number
  account_id?: number
  model?: string
  kind?: CodexTicketEventKind
  occurred_at?: string
  recorded_at?: string
  outcome?: CodexTicketAttemptOutcome
  trigger?: string
  http_status?: number
  duration_ms?: number
  reason_code?: string
  proxy_id?: number
  proxy_name?: string
  expires_at?: string
  ticket_length?: number
  ticket_generation_id?: string
  verification_method?: string
  turn_state_present?: boolean
  cookie_present?: boolean
  request_kind?: string
  request_route?: string
  invalidated_current?: boolean
  original_ticket_present?: boolean
  original_cookie_present?: boolean
  returned_cookie_present?: boolean
  returned_set_cookie_count?: number
  [key: string]: unknown
}

/** GET /admin/accounts/:id/codex-ticket-events */
export interface CodexTicketEventsResponse {
  items: CodexTicketEvent[] | null
  total: number
  page: number
  page_size: number
}

/** 失效流水列表项：只回传存在性布尔，不回传票据本体。 */
export interface CodexTicketInvalidationSummary {
  id: number
  account_id: number
  model: string
  ticket_generation_id: string
  occurred_at: string
  recorded_at: string
  reason_code: string
  response_http_status: number | null
  request_kind: string
  request_route: string
  invalidated_current: boolean
  original_ticket_present: boolean
  original_cookie_present: boolean
  returned_cookie_present: boolean
  returned_set_cookie_count: number
}

/** GET /admin/accounts/:id/codex-ticket-invalidations */
export interface CodexTicketInvalidationsResponse {
  items: CodexTicketInvalidationSummary[] | null
  total: number
  page: number
  page_size: number
}

/** 单条失效流水详情（显式请求时后端才会返回票据片段）。 */
export interface CodexTicketInvalidationDetail extends CodexTicketInvalidationSummary {
  original_ticket?: string | null
  original_cookie?: string | null
  returned_ticket?: string
  returned_cookie?: string | null
  returned_set_cookies?: string[]
}

/** 打票代理池设置：all=全部未过期代理，custom=只轮换 proxy_ids。 */
export interface CodexTicketProxyPool {
  mode: 'all' | 'custom'
  proxy_ids: number[]
}

/** 打票节奏：失败退避区间与成功票据主动刷新周期。 */
export interface CodexTicketCadence {
  retry_min_seconds: number
  retry_max_seconds: number
  refresh_seconds: number
}

/** GET /admin/codex-tickets/settings */
export interface CodexTicketSettings {
  enabled: boolean
  allow_without_ticket: boolean
  cadence: CodexTicketCadence
  pool: CodexTicketProxyPool
  /** 当前可参与打票轮换的代理（active 且未过期）。 */
  available_proxies: Proxy[] | null
}

/** 手动打票结果。 */
export interface CodexTicketHarvestResult extends CodexTicketAttempt {
  ticket_status: CodexTicketStatus
  history_recorded: boolean
}

export type CodexTicketHistoryFilter = 'all' | 'success'
export type CodexTicketEventFilter = 'all' | 'attempts' | 'success' | 'failure' | 'invalidation'

export interface CodexTicketPageQuery {
  page?: number
  page_size?: number
  model?: string
  /** RFC3339，默认最近 90 天。 */
  start_time?: string
  end_time?: string
}

function pickDefined(source: object): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries(source).filter(([, value]) => value !== undefined && value !== null && value !== '')
  )
}

/** 获取账号在全部打票模型上的当前票据摘要。 */
export async function getAccountTicketStatuses(
  accountId: number
): Promise<CodexTicketStatusesResponse> {
  const { data } = await apiClient.get<CodexTicketStatusesResponse>(
    `/admin/accounts/${accountId}/codex-tickets`
  )
  return data
}

/** 打票流水（filter=all|success，可按 model 过滤）。 */
export async function getTicketHistory(
  accountId: number,
  query: CodexTicketPageQuery & { filter?: CodexTicketHistoryFilter } = {}
): Promise<CodexTicketHistoryResponse> {
  const { data } = await apiClient.get<CodexTicketHistoryResponse>(
    `/admin/accounts/${accountId}/codex-ticket-history`,
    { params: pickDefined(query) }
  )
  return data
}

/** attempt + invalidation 合并时间线。 */
export async function getTicketEvents(
  accountId: number,
  query: CodexTicketPageQuery & { filter?: CodexTicketEventFilter } = {}
): Promise<CodexTicketEventsResponse> {
  const { data } = await apiClient.get<CodexTicketEventsResponse>(
    `/admin/accounts/${accountId}/codex-ticket-events`,
    { params: pickDefined(query) }
  )
  return data
}

/** 手动打一发票（可能耗时数十秒，调用方需自行放宽超时）。 */
export async function harvestTicket(
  accountId: number,
  model: string
): Promise<CodexTicketHarvestResult> {
  const { data } = await apiClient.post<CodexTicketHarvestResult>(
    `/admin/accounts/${accountId}/codex-ticket-harvest`,
    { model },
    { timeout: 0 }
  )
  return data
}

/** 保存账号级 / 逐模型打票参与开关。 */
export async function setTicketParticipation(
  accountId: number,
  payload: { enabled: boolean; models: Record<string, boolean> }
): Promise<{ enabled: boolean; models: Record<string, boolean> }> {
  const { data } = await apiClient.put<{ enabled: boolean; models: Record<string, boolean> }>(
    `/admin/accounts/${accountId}/codex-ticket-participation`,
    payload
  )
  return data
}

/** 票据失效流水列表。 */
export async function listTicketInvalidations(
  accountId: number,
  query: CodexTicketPageQuery = {}
): Promise<CodexTicketInvalidationsResponse> {
  const { data } = await apiClient.get<CodexTicketInvalidationsResponse>(
    `/admin/accounts/${accountId}/codex-ticket-invalidations`,
    { params: pickDefined(query) }
  )
  return data
}

/** 单条失效流水详情。 */
export async function getTicketInvalidation(
  accountId: number,
  eventId: number
): Promise<CodexTicketInvalidationDetail> {
  const { data } = await apiClient.get<CodexTicketInvalidationDetail>(
    `/admin/accounts/${accountId}/codex-ticket-invalidations/${eventId}`
  )
  return data
}

/** 读取打票全局设置（开关 / 放行 / 节奏 / 代理池 / 可用代理）。 */
export async function getTicketSettings(): Promise<CodexTicketSettings> {
  const { data } = await apiClient.get<CodexTicketSettings>('/admin/codex-tickets/settings')
  return data
}

/** 全局打票开关。 */
export async function setTicketEnabled(enabled: boolean): Promise<{ enabled: boolean }> {
  const { data } = await apiClient.put<{ enabled: boolean }>('/admin/codex-tickets/enabled', {
    enabled
  })
  return data
}

/** 无票是否放行。 */
export async function setTicketAllowWithoutTicket(
  allow: boolean
): Promise<{ allow_without_ticket: boolean }> {
  const { data } = await apiClient.put<{ allow_without_ticket: boolean }>(
    '/admin/codex-tickets/allow-without-ticket',
    { allow }
  )
  return data
}

/** 读取打票代理池。 */
export async function getTicketProxyPool(): Promise<CodexTicketProxyPool> {
  const { data } = await apiClient.get<CodexTicketProxyPool>('/admin/codex-tickets/pool')
  return data
}

/** 保存打票代理池。 */
export async function setTicketProxyPool(pool: CodexTicketProxyPool): Promise<CodexTicketProxyPool> {
  const { data } = await apiClient.put<CodexTicketProxyPool>('/admin/codex-tickets/pool', pool)
  return data
}

/** 读取打票节奏。 */
export async function getTicketCadence(): Promise<CodexTicketCadence> {
  const { data } = await apiClient.get<CodexTicketCadence>('/admin/codex-tickets/cadence')
  return data
}

/** 保存打票节奏。 */
export async function setTicketCadence(cadence: CodexTicketCadence): Promise<CodexTicketCadence> {
  const { data } = await apiClient.put<CodexTicketCadence>('/admin/codex-tickets/cadence', cadence)
  return data
}

export default {
  getAccountTicketStatuses,
  getTicketHistory,
  getTicketEvents,
  harvestTicket,
  setTicketParticipation,
  listTicketInvalidations,
  getTicketInvalidation,
  getTicketSettings,
  setTicketEnabled,
  setTicketAllowWithoutTicket,
  getTicketProxyPool,
  setTicketProxyPool,
  getTicketCadence,
  setTicketCadence
}
