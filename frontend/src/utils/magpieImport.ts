import type { GroupPlatform } from '@/types'

/**
 * magpie 的导入链接：`magpie://import?name=...&<协议>=<base url>...`
 *
 * 与 cc-switch 的差异（均来自实测，见 magpieImport.spec.ts 的用例说明）：
 * - magpie 没有“目标客户端”概念，一个供应商可以同时登记多个协议端点；
 * - 端点按协议分键：`chat` / `responses` / `anthropic` / `gemini`
 *   （`systemone` 在 GUI 里存在，但链接不接受，只能手工添加）；
 * - 每个协议期望的 base 形态不同：chat/responses 含 `/v1`，anthropic/gemini 不含；
 * - `id=` 可选，用来给供应商一个稳定的 ASCII 标识（模型路由里写成 `id/model`）。
 */
export type MagpieProtocol = 'chat' | 'responses' | 'anthropic' | 'gemini'

export interface MagpieEndpoint {
  protocol: MagpieProtocol
  /** 已按 magpie 期望形态归一化好的 base URL。 */
  baseUrl: string
}

export interface MagpieImportInput {
  /** sub2api 的对外基址，例如 https://api.example.com */
  baseUrl: string
  platform?: GroupPlatform | null
  /** 供应商标识，建议 `sub2api-<分组 id>`；缺省时 magpie 会从 name 推导。 */
  providerId?: string
  /** 供应商显示名，允许中文与空格。 */
  providerName: string
  apiKey: string
  /** 可选：直接声明模型 id，省掉 magpie 添加后的自动探测。 */
  models?: string[]
  /** 是否附带余额查询（sub2api 的 /v1/usage 顶层返回 remaining）。 */
  includeBalance?: boolean
  icon?: string
}

/** 模型列表上限：太长的 `models=` 会让链接超出协议处理程序的长度限制。 */
export const MAGPIE_MAX_MODELS = 20

/** 链接总长上限：超过就把可省略的参数丢掉，保证链接还能被拉起来。 */
export const MAGPIE_MAX_LINK_LENGTH = 2000

/**
 * 分组平台 → magpie 协议端点。
 *
 * sub2api 的 /v1/messages、/v1/responses、/v1/chat/completions、/v1beta 对所有
 * 分组都注册了路由（中间件只要求 key 已绑定分组），但“路由通”不等于“该分组有
 * 能承接的账号”。所以这里只登记该平台**原生**协议 + 通用的 chat，避免在 magpie
 * 里造出这个分组根本走不通的死端点。
 */
const PLATFORM_PROTOCOLS: Record<string, MagpieProtocol[]> = {
  anthropic: ['anthropic', 'chat'],
  openai: ['responses', 'chat'],
  openai_bps: ['responses', 'chat'],
  gemini: ['gemini', 'chat'],
  antigravity: ['anthropic', 'gemini', 'chat'],
  composite: ['chat']
}

const DEFAULT_PROTOCOLS: MagpieProtocol[] = ['chat']

/**
 * 无法用 magpie 链接表达的平台。
 *
 * `typesafe`（System One）是独立协议：后端 `gateway_systemone.go:76` 明确要求
 * 分组平台是 typesafe/composite，而 `PlatformTypeSafe` 不出现在
 * `/v1/chat/completions`、`/v1/messages` 的分支里——chat/anthropic 端点对它都是
 * 死路。magpie 有 System One 页签，但链接与 CLI 都不接受 `systemone=`
 * （`unknown field "systemone"`），只能在其 GUI 里手工添加。
 */
export const MAGPIE_UNSUPPORTED_PLATFORMS: ReadonlySet<string> = new Set(['typesafe'])

export function isMagpieImportSupported(platform?: GroupPlatform | null): boolean {
  if (!platform) {
    return true
  }
  return !MAGPIE_UNSUPPORTED_PLATFORMS.has(platform)
}

export function resolveMagpieProtocols(platform?: GroupPlatform | null): MagpieProtocol[] {
  if (!platform) {
    return DEFAULT_PROTOCOLS
  }
  if (!isMagpieImportSupported(platform)) {
    return []
  }
  return PLATFORM_PROTOCOLS[platform] ?? DEFAULT_PROTOCOLS
}

/**
 * antigravity 分组挂在独立的 `/antigravity` 前缀下（见后端 routes/gateway.go 的
 * antigravityV1 / antigravityV1Beta 路由组），其余平台共用根路径。
 */
export function magpieProviderRoot(baseUrl: string, platform?: GroupPlatform | null): string {
  const trimmed = (baseUrl || '').replace(/\/+$/, '')
  if (platform === 'antigravity') {
    return `${trimmed}/antigravity`
  }
  return trimmed
}

/**
 * magpie 会自己在 base 后面拼它的路径，四种协议的 base 形态不一样：
 * - chat / responses：magpie 探测 `<base>/models`，故 base 需含 `/v1`；
 * - anthropic：magpie 探测 `<base>/models` 与 `<base>/v1/models`，故 base 不含 `/v1`；
 * - gemini：magpie 探测 `<base>/v1beta/models`，故 base 不含版本段。
 */
export function magpieEndpointBaseUrl(protocol: MagpieProtocol, root: string): string {
  switch (protocol) {
    case 'chat':
    case 'responses':
      return `${root}/v1`
    case 'anthropic':
    case 'gemini':
    default:
      return root
  }
}

export function resolveMagpieEndpoints(input: Pick<MagpieImportInput, 'baseUrl' | 'platform'>): MagpieEndpoint[] {
  const root = magpieProviderRoot(input.baseUrl, input.platform)
  if (!root) {
    return []
  }
  return resolveMagpieProtocols(input.platform).map((protocol) => ({
    protocol,
    baseUrl: magpieEndpointBaseUrl(protocol, root)
  }))
}

/** sub2api 的余额查询地址：/v1/usage 顶层返回 remaining。 */
export function magpieBalanceUrl(input: Pick<MagpieImportInput, 'baseUrl' | 'platform'>): string {
  return `${magpieProviderRoot(input.baseUrl, input.platform)}/v1/usage`
}

export function limitMagpieModels(models?: string[]): string[] {
  if (!models?.length) {
    return []
  }
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of models) {
    const model = (raw || '').trim()
    if (!model || seen.has(model)) {
      continue
    }
    seen.add(model)
    out.push(model)
    if (out.length >= MAGPIE_MAX_MODELS) {
      break
    }
  }
  return out
}

/** 稳定标识：分组 id 全局唯一，路由里写 `sub2api-65/claude-sonnet-4.5` 不会漂移。 */
export function magpieProviderId(groupId: number | string | null | undefined): string {
  if (groupId === null || groupId === undefined || groupId === '') {
    return ''
  }
  return `sub2api-${groupId}`
}

export function buildMagpieImportDeeplink(input: MagpieImportInput): string {
  const endpoints = resolveMagpieEndpoints(input)
  if (endpoints.length === 0) {
    return ''
  }

  const params = new URLSearchParams()
  const name = (input.providerName || '').trim()
  if (name) {
    params.set('name', name)
  }
  const id = (input.providerId || '').trim()
  if (id) {
    params.set('id', id)
  }
  for (const endpoint of endpoints) {
    params.set(endpoint.protocol, endpoint.baseUrl)
  }
  if (input.apiKey) {
    params.set('key', input.apiKey)
  }
  if (input.icon) {
    params.set('icon', input.icon)
  }
  if (input.includeBalance) {
    params.set('balance', magpieBalanceUrl(input))
    params.set('balance.path', 'remaining')
  }

  const models = limitMagpieModels(input.models)
  const base = `magpie://import?${params.toString()}`
  if (models.length === 0) {
    return base
  }
  params.set('models', models.join(','))
  const withModels = `magpie://import?${params.toString()}`
  // 链接过长时宁可不带 models：magpie 会自己探测，而超长链接可能被系统直接丢弃。
  return withModels.length <= MAGPIE_MAX_LINK_LENGTH ? withModels : base
}

/** 单引号安全包裹，供 shell 里直接粘贴执行。 */
function shellSingleQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`
}

/**
 * 批量导入用的一段命令：magpie CLI 的 `import -y` 会跳过确认直接写入，
 * 比连着拉起 N 次协议确认框顺手得多。
 */
export function buildMagpieBatchScript(links: string[]): string {
  const usable = links.filter((link) => !!link)
  if (usable.length === 0) {
    return ''
  }
  return usable.map((link) => `magpie import -y ${shellSingleQuote(link)}`).join(' && ')
}
