/**
 * OpenAI 账号「请求时区」的纯函数。
 *
 * 后端把账号级请求时区保存在 accounts.extra.openai_request_timezone：
 *   - 未设置 / 空串 = 使用默认时区（GET /admin/accounts/openai-request-timezones 的 default）
 *   - 非 OpenAI 平台不允许配置非空时区（保存会被后端拒绝）
 * 这里集中处理下拉选项构造、历史值归一化与 extra 写入/清理，便于单测。
 */

/** 与后端 service.DefaultOpenAIRequestTimezone 对齐的兜底默认值。 */
export const DEFAULT_OPENAI_REQUEST_TIMEZONE = 'Asia/Singapore'

export const OPENAI_REQUEST_TIMEZONE_EXTRA_KEY = 'openai_request_timezone'

export interface OpenAIRequestTimezoneOption {
  label: string
  value: string
  [key: string]: unknown
}

/**
 * 构造下拉选项：第一项是「跟随默认」的空值选项，其后是后端返回的时区列表。
 * 列表为空时仍保留默认项，避免下拉变成不可用状态。
 */
export function buildOpenAIRequestTimezoneOptions(
  defaultTimezone: string | null | undefined,
  timezones: string[] | null | undefined,
  defaultLabel: string
): OpenAIRequestTimezoneOption[] {
  const fallback = (defaultTimezone || '').trim() || DEFAULT_OPENAI_REQUEST_TIMEZONE
  const options: OpenAIRequestTimezoneOption[] = [
    { label: `${defaultLabel} (${fallback})`, value: '' }
  ]
  const seen = new Set<string>()
  for (const raw of timezones ?? []) {
    const value = typeof raw === 'string' ? raw.trim() : ''
    if (!value || seen.has(value)) continue
    seen.add(value)
    options.push({ label: value, value })
  }
  return options
}

/**
 * 把账号 extra 里的历史值归一化成下拉可用的值：
 * 非字符串、空串或不在允许列表内的一律回落到 ''（跟随默认）。
 * 注意：不在列表内但仍是合法 IANA 时区（例如后端列表调整过）也回落到默认，
 * 避免 UI 提交一个后端会拒绝的值。
 */
export function normalizeOpenAIRequestTimezone(
  raw: unknown,
  timezones: string[] | null | undefined
): string {
  if (typeof raw !== 'string') return ''
  const value = raw.trim()
  if (!value) return ''
  if (!Array.isArray(timezones) || timezones.length === 0) return value
  return timezones.includes(value) ? value : ''
}

/**
 * 把请求时区写入 extra（就地修改并返回同一个对象）：
 *   - 非 OpenAI 平台 / 空值：删除键（表示使用默认时区）
 *   - OpenAI 平台且非空：写入字符串
 */
export function applyOpenAIRequestTimezoneExtra(
  extra: Record<string, unknown>,
  platform: string | null | undefined,
  timezone: string | null | undefined
): Record<string, unknown> {
  const value = typeof timezone === 'string' ? timezone.trim() : ''
  if (platform !== 'openai' || !value) {
    delete extra[OPENAI_REQUEST_TIMEZONE_EXTRA_KEY]
    return extra
  }
  extra[OPENAI_REQUEST_TIMEZONE_EXTRA_KEY] = value
  return extra
}
