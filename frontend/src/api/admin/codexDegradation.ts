/**
 * Admin Codex Degradation (降智检测) API endpoints
 *
 * 契约来源：backend/internal/handler/admin/codex_diagnostic_handler.go
 * 与 backend/internal/service/codex_auto_degradation_check.go。
 */

import { apiClient } from '../client'

/** 四态判定。 */
export type CodexDegradationStatus = 'normal' | 'degraded' | 'uncertain' | 'failed'

/** 单个模型的一次探测判定。 */
export interface CodexDegradationProbeResult {
  model: string
  status: CodexDegradationStatus
  /** 未获得预测时省略。 */
  predicted_model?: string
  /** 0..1，未获得预测时省略。 */
  probability?: number
  parsed_count?: number
  reason?: string
  http_status?: number
}

/** POST /admin/codex-degradation/accounts/:id/diagnose 的响应。 */
export interface CodexDegradationCheckResult {
  account_id: number
  status: CodexDegradationStatus
  reason?: string
  checked_at: string
  models: string[] | null
  items: CodexDegradationProbeResult[] | null
}

/** 一条持久化的历史探测结果。 */
export interface CodexDegradationProbeResultRecord {
  account_id: number
  model: string
  status: CodexDegradationStatus
  reason?: string
  predicted_model?: string
  probability: number
  parsed_count: number
  http_status: number
  source: string
  created_at: string
}

/** 自动降智检测设置。 */
export interface CodexDegradationSettings {
  enabled: boolean
  interval_minutes: number
  block_minutes: number
  models: string[]
}

/** GET /admin/codex-degradation/models */
export interface CodexDegradationModelsResponse {
  models: string[] | null
  /** 内嵌 ModelTrace 指纹库提交号。 */
  bank: string
}

/** GET /admin/codex-degradation/probe-template */
export interface CodexProbeTemplateResponse {
  /** 当前生效的 JSONL 原文；空串表示使用内嵌默认模板。 */
  template: string
  default_template: string
}

/** GET /admin/codex-degradation/accounts/:id/results */
export interface CodexDegradationResultsResponse {
  items: CodexDegradationProbeResultRecord[] | null
}

/** 可检测模型列表与指纹库版本。 */
export async function listDegradationModels(): Promise<CodexDegradationModelsResponse> {
  const { data } = await apiClient.get<CodexDegradationModelsResponse>(
    '/admin/codex-degradation/models'
  )
  return data
}

/** 读取自动降智检测设置。 */
export async function getDegradationSettings(): Promise<CodexDegradationSettings> {
  const { data } = await apiClient.get<CodexDegradationSettings>(
    '/admin/codex-degradation/settings'
  )
  return data
}

/** 保存自动降智检测设置。 */
export async function saveDegradationSettings(
  settings: CodexDegradationSettings
): Promise<CodexDegradationSettings> {
  const { data } = await apiClient.put<CodexDegradationSettings>(
    '/admin/codex-degradation/settings',
    settings
  )
  return data
}

/** 读取可编辑的 JSONL 探测模板与内嵌默认模板。 */
export async function getProbeTemplate(): Promise<CodexProbeTemplateResponse> {
  const { data } = await apiClient.get<CodexProbeTemplateResponse>(
    '/admin/codex-degradation/probe-template'
  )
  return data
}

/** 保存 JSONL 探测模板；模板非法时后端拒绝保存。 */
export async function saveProbeTemplate(template: string): Promise<{ saved: boolean }> {
  const { data } = await apiClient.put<{ saved: boolean }>(
    '/admin/codex-degradation/probe-template',
    { template }
  )
  return data
}

/** 立即触发一轮自动检测（设置关闭时不会产生探测）。 */
export async function runDegradationNow(): Promise<{ triggered: boolean }> {
  const { data } = await apiClient.post<{ triggered: boolean }>(
    '/admin/codex-degradation/run',
    undefined,
    { timeout: 0 }
  )
  return data
}

/**
 * 用目标账号自身凭据逐个模型执行原生探测。
 * 单模型最长 120s，多模型时总耗时可观，故显式关闭 axios 超时。
 */
export async function diagnoseAccount(
  accountId: number,
  models: string[]
): Promise<CodexDegradationCheckResult> {
  const { data } = await apiClient.post<CodexDegradationCheckResult>(
    `/admin/codex-degradation/accounts/${accountId}/diagnose`,
    { models },
    { timeout: 0 }
  )
  return data
}

/** 账号最近的探测结果（默认 50 条，后端上限 200）。 */
export async function listAccountDegradationResults(
  accountId: number,
  limit = 50
): Promise<CodexDegradationResultsResponse> {
  const { data } = await apiClient.get<CodexDegradationResultsResponse>(
    `/admin/codex-degradation/accounts/${accountId}/results`,
    { params: { limit } }
  )
  return data
}

export default {
  listDegradationModels,
  getDegradationSettings,
  saveDegradationSettings,
  getProbeTemplate,
  saveProbeTemplate,
  runDegradationNow,
  diagnoseAccount,
  listAccountDegradationResults
}
