import type { OpenAIBPSCredentialState, OpenAIBPSCredentialStatus } from '@/types'

/**
 * OpenAI BPS 前端纯逻辑：平台判断、凭证构造、凭证状态展示元数据。
 *
 * 与后端 service 层的契约保持一致：
 * - 平台值固定为 `openai_bps`；
 * - 账号类型固定为 oauth，凭证只有 access_token（必填，编辑留空=保留）与
 *   chatgpt_account_id（可选）；
 * - 编辑提交采用"全对象替换 + 敏感键保留"语义：access_token 为空时不能提交
 *   空字符串（否则会被当成显式清空），必须省略该键。
 */

export const OPENAI_BPS_PLATFORM = 'openai_bps'
export const OPENAI_BPS_ACCESS_TOKEN_KEY = 'access_token'
export const OPENAI_BPS_ACCOUNT_ID_KEY = 'chatgpt_account_id'
/** 后端脱敏占位符；回传时按"未提供"处理。 */
export const OPENAI_BPS_REDACTED_TOKEN = '***'

export function isOpenAIBPSPlatform(platform: string | null | undefined): boolean {
  return (platform ?? '').trim().toLowerCase() === OPENAI_BPS_PLATFORM
}

/** 归一化 Access Token：去空白、剥离误粘贴的 `Bearer ` 前缀。 */
export function normalizeOpenAIBPSAccessToken(raw: unknown): string {
  if (typeof raw !== 'string') return ''
  let token = raw.trim()
  if (token.toLowerCase().startsWith('bearer ')) {
    token = token.slice('bearer '.length).trim()
  }
  if (token === OPENAI_BPS_REDACTED_TOKEN) return ''
  return token
}

/** 归一化 chatgpt_account_id：去空白；含空白字符视为非法（返回空串由调用方校验）。 */
export function normalizeOpenAIBPSAccountID(raw: unknown): string {
  if (typeof raw !== 'string') return ''
  return raw.trim()
}

/**
 * 校验 BPS 表单。
 * - create：access_token 必填；
 * - edit：access_token 可留空（保留原 token），但若填写则必须合法；
 * - chatgpt_account_id 可选，但不能包含空白字符。
 * 返回首个错误的 i18n key 后缀（'tokenRequired' / 'accountIdInvalid'），无错误返回 null。
 */
export function validateOpenAIBPSForm(
  input: { accessToken?: unknown; chatgptAccountId?: unknown },
  mode: 'create' | 'edit'
): 'tokenRequired' | 'accountIdInvalid' | null {
  const token = normalizeOpenAIBPSAccessToken(input.accessToken)
  if (!token && mode === 'create') return 'tokenRequired'
  const accountId = normalizeOpenAIBPSAccountID(input.chatgptAccountId)
  if (accountId && /\s/.test(accountId)) return 'accountIdInvalid'
  return null
}

/**
 * 构造 BPS 账号提交的 credentials。
 *
 * - access_token 为空时省略该键（create 由校验拦住；edit 表示保留原 token）；
 * - chatgpt_account_id 为空时省略该键（edit 语义 = 清空，因为非敏感键全量替换）。
 */
export function buildOpenAIBPSCredentials(input: {
  accessToken?: unknown
  chatgptAccountId?: unknown
}): Record<string, unknown> {
  const credentials: Record<string, unknown> = {}
  const token = normalizeOpenAIBPSAccessToken(input.accessToken)
  if (token) {
    credentials[OPENAI_BPS_ACCESS_TOKEN_KEY] = token
  }
  const accountId = normalizeOpenAIBPSAccountID(input.chatgptAccountId)
  if (accountId) {
    credentials[OPENAI_BPS_ACCOUNT_ID_KEY] = accountId
  }
  return credentials
}

export interface OpenAIBPSCredentialStatusMeta {
  /** 凭证状态原始值。 */
  status: OpenAIBPSCredentialStatus
  /** i18n 状态文案 key 后缀（admin.accounts.bps.credentialStatus.*）。 */
  labelKey: OpenAIBPSCredentialStatus
  /** Tailwind 徽标配色。 */
  badgeClass: string
  /** 小圆点配色。 */
  dotClass: string
  /** 是否为异常状态（过期 / 撤销 / 认证失败）。 */
  abnormal: boolean
}

const BPS_STATUS_META: Record<OpenAIBPSCredentialStatus, Omit<OpenAIBPSCredentialStatusMeta, 'status' | 'labelKey'>> = {
  normal: {
    badgeClass: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300',
    dotClass: 'bg-emerald-500',
    abnormal: false
  },
  expired: {
    badgeClass: 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300',
    dotClass: 'bg-amber-500',
    abnormal: true
  },
  revoked: {
    badgeClass: 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300',
    dotClass: 'bg-red-500',
    abnormal: true
  },
  auth_failed: {
    badgeClass: 'bg-orange-100 text-orange-800 dark:bg-orange-900/30 dark:text-orange-300',
    dotClass: 'bg-orange-500',
    abnormal: true
  }
}

/**
 * 返回 BPS 凭证状态展示元数据。
 * 非 BPS 账号 / 无状态（旧数据或后端未返回）返回 null，调用方据此不渲染徽标。
 */
export function openAIBPSCredentialStatusMeta(
  platform: string | null | undefined,
  state: OpenAIBPSCredentialState | null | undefined
): OpenAIBPSCredentialStatusMeta | null {
  if (!isOpenAIBPSPlatform(platform)) return null
  const status = state?.status
  if (!status || !(status in BPS_STATUS_META)) return null
  return { status, labelKey: status, ...BPS_STATUS_META[status] }
}

/** 凭证状态 tooltip 的附加信息：到期时间 / 最近发现时间 / 错误码。 */
export function openAIBPSCredentialStatusDetails(
  state: OpenAIBPSCredentialState | null | undefined
): { expiresAt: string; lastSeenAt: string; errorCode: string } {
  return {
    expiresAt: typeof state?.expires_at === 'string' ? state.expires_at : '',
    lastSeenAt: typeof state?.last_seen_at === 'string' ? state.last_seen_at : '',
    errorCode: typeof state?.error_code === 'string' ? state.error_code : ''
  }
}
