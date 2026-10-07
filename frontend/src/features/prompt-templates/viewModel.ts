/**
 * 文本提示词管理的纯函数工具：枚举常量、输入解析、版本差异与错误判定。
 *
 * 这里刻意不引入任何新依赖，差异展示用行集合比较实现，只回答
 * “哪些行新增/删除、模型与 profile 增删了什么”，不做行内 diff。
 */

import type {
  PromptProfileId,
  PromptReason,
  PromptTemplateVersion,
  PromptValidationReport,
} from './types'

/** 后端支持的协议 profile ID（顺序与后端预览顺序一致）。 */
export const PROMPT_PROFILES: PromptProfileId[] = [
  'chat_http',
  'responses_http',
  'chat_to_responses',
  'responses_to_chat',
]

/** 运行期记录固定原因枚举（顺序与后端常量一致）。 */
export const PROMPT_REASONS: PromptReason[] = [
  'applied',
  'disabled',
  'group_disabled',
  'account_disabled',
  'skipped_model_scope',
  'skipped_non_text_task',
  'unsupported_profile',
  'unsupported_history_mode',
  'no_compatible_account',
  'config_unavailable',
  'invalid_config',
  'adapter_error',
]

/** 正文上限（与后端 PromptBodyMaxBytes 一致），仅用于前端提示。 */
export const PROMPT_BODY_MAX_BYTES = 32 * 1024

/** 把多行/逗号分隔的输入解析成去重后的字符串数组。 */
export function parseStringList(input: string): string[] {
  const seen = new Set<string>()
  const result: string[] = []
  for (const raw of input.split(/[\n,]/)) {
    const value = raw.trim()
    if (!value || seen.has(value)) continue
    seen.add(value)
    result.push(value)
  }
  return result
}

/** 把字符串数组格式化成便于编辑的多行文本。 */
export function formatStringList(values: string[] | null | undefined): string {
  return (values ?? []).join('\n')
}

/** 计算 UTF-8 字节数（用于与后端 body_bytes 对齐的前端提示）。 */
export function utf8ByteLength(value: string): number {
  if (typeof TextEncoder !== 'undefined') {
    return new TextEncoder().encode(value).length
  }
  // 退化实现：按码点估算，仅在没有 TextEncoder 的环境使用。
  let bytes = 0
  for (const char of value) {
    const code = char.codePointAt(0) ?? 0
    if (code <= 0x7f) bytes += 1
    else if (code <= 0x7ff) bytes += 2
    else if (code <= 0xffff) bytes += 3
    else bytes += 4
  }
  return bytes
}

/** 版本显示名。 */
export function versionLabel(version: Pick<PromptTemplateVersion, 'version_no'> | null | undefined): string {
  if (!version) return ''
  return `v${version.version_no}`
}

/** 判断错误是否是乐观锁冲突（409 / PROMPT_REVISION_CONFLICT）。 */
export function isRevisionConflict(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false
  const candidate = error as { status?: number; reason?: string; code?: string | number; message?: string }
  if (candidate.status === 409) return true
  if (candidate.reason === 'PROMPT_REVISION_CONFLICT') return true
  if (candidate.code === 'PROMPT_REVISION_CONFLICT') return true
  return false
}

/** 判断错误是否是“模板已归档”。 */
export function isTemplateArchivedError(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false
  const candidate = error as { reason?: string; code?: string | number; status?: number }
  return (
    candidate.reason === 'PROMPT_TEMPLATE_ARCHIVED' ||
    candidate.code === 'PROMPT_TEMPLATE_ARCHIVED' ||
    candidate.status === 409
  )
}

/** 原因枚举 → i18n key 后缀；未知值统一落到 unknown。 */
export function promptReasonKey(reason: string): PromptReason | 'unknown' {
  return (PROMPT_REASONS as string[]).includes(reason) ? (reason as PromptReason) : 'unknown'
}

/** profile ID → i18n key 后缀。 */
export function promptProfileKey(profile: string): string {
  return (PROMPT_PROFILES as string[]).includes(profile) ? profile : 'unknown'
}

/** 策略来源 → i18n key 后缀。 */
export function bindingSourceKey(source: string | undefined): string {
  if (source === 'group' || source === 'account_override') return source
  return 'unknown'
}

/** 草稿是否通过发布前校验。 */
export function isValidationBlocking(report: PromptValidationReport | null): boolean {
  return !report || !report.valid
}

/** 取校验报告中的错误列表（后端可能返回 null）。 */
export function validationErrors(report: PromptValidationReport | null): string[] {
  return report?.errors ?? []
}

/**
 * 从后端原始校验错误字符串里提取 reason code。
 *
 * 后端把 `infraerrors.BadRequest(...)` 的 `Error()` 直接放进 errors 数组，
 * 形如 `error: code=400 reason="PROMPT_SCOPE_EMPTY_MODELS" message="..."`，
 * 因此这里只抽取 reason 用于 i18n 映射，取不到就回退展示原文。
 */
export function validationReasonCode(error: string): string | null {
  const match = /reason="([A-Za-z0-9_]+)"/.exec(error)
  return match ? match[1] : null
}

export interface VersionDiff {
  bodyChanged: boolean
  addedLines: string[]
  removedLines: string[]
  clientModelsAdded: string[]
  clientModelsRemoved: string[]
  upstreamModelsAdded: string[]
  upstreamModelsRemoved: string[]
  profilesAdded: string[]
  profilesRemoved: string[]
  hasChanges: boolean
}

function diffSets(current: string[], previous: string[]): { added: string[]; removed: string[] } {
  const currentSet = new Set(current)
  const previousSet = new Set(previous)
  return {
    added: current.filter((item) => !previousSet.has(item)),
    removed: previous.filter((item) => !currentSet.has(item)),
  }
}

function diffLines(currentBody: string, previousBody: string): { added: string[]; removed: string[] } {
  const currentLines = currentBody.split('\n')
  const previousLines = previousBody.split('\n')
  const currentCounts = new Map<string, number>()
  for (const line of currentLines) {
    currentCounts.set(line, (currentCounts.get(line) ?? 0) + 1)
  }
  const previousCounts = new Map<string, number>()
  for (const line of previousLines) {
    previousCounts.set(line, (previousCounts.get(line) ?? 0) + 1)
  }

  const added: string[] = []
  for (const [line, count] of currentCounts) {
    const before = previousCounts.get(line) ?? 0
    for (let i = before; i < count; i += 1) added.push(line)
  }
  const removed: string[] = []
  for (const [line, count] of previousCounts) {
    const after = currentCounts.get(line) ?? 0
    for (let i = after; i < count; i += 1) removed.push(line)
  }
  return { added, removed }
}

/**
 * 计算两个版本之间的差异。
 *
 * previous 为 null 时表示这是第一个版本：所有内容都算新增。
 */
export function diffVersions(
  current: PromptTemplateVersion,
  previous: PromptTemplateVersion | null,
): VersionDiff {
  const lines = diffLines(current.body, previous?.body ?? '')
  const clientModels = diffSets(current.client_models ?? [], previous?.client_models ?? [])
  const upstreamModels = diffSets(current.upstream_models ?? [], previous?.upstream_models ?? [])
  const profiles = diffSets(current.supported_profiles ?? [], previous?.supported_profiles ?? [])

  const bodyChanged = lines.added.length > 0 || lines.removed.length > 0
  const hasChanges =
    bodyChanged ||
    clientModels.added.length > 0 ||
    clientModels.removed.length > 0 ||
    upstreamModels.added.length > 0 ||
    upstreamModels.removed.length > 0 ||
    profiles.added.length > 0 ||
    profiles.removed.length > 0

  return {
    bodyChanged,
    addedLines: lines.added,
    removedLines: lines.removed,
    clientModelsAdded: clientModels.added,
    clientModelsRemoved: clientModels.removed,
    upstreamModelsAdded: upstreamModels.added,
    upstreamModelsRemoved: upstreamModels.removed,
    profilesAdded: profiles.added,
    profilesRemoved: profiles.removed,
    hasChanges,
  }
}

/** 生成一次发布的幂等键，避免双击产生两个版本。 */
export function newIdempotencyKey(templateId: number): string {
  const random =
    typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(16).slice(2)}`
  return `pt-${templateId}-${random}`.slice(0, 64)
}

/** 判断草稿是否被本地修改（与服务器快照比较）。 */
export function draftFingerprint(draft: {
  body: string
  client_models: string[]
  upstream_models: string[]
  supported_profiles: string[]
} | null): string {
  if (!draft) return ''
  return JSON.stringify({
    body: draft.body,
    client_models: [...(draft.client_models ?? [])].sort(),
    upstream_models: [...(draft.upstream_models ?? [])].sort(),
    supported_profiles: [...(draft.supported_profiles ?? [])].sort(),
  })
}
