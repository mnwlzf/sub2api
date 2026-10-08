/**
 * 文本提示词管理（模板 / 草稿 / 不可变版本 / 分组绑定 / 账号覆盖）的前端类型定义。
 *
 * 字段名与 JSON tag 严格对应后端
 * backend/internal/service/prompt_template.go 与 prompt_request_event.go。
 */

/** 分组绑定模式：disabled 或绑定到某个固定版本。 */
export type PromptBindingMode = 'disabled' | 'version'

/** 分组内账号覆盖模式。 */
export type PromptOverrideMode = 'inherit' | 'disabled' | 'version'

/** 协议适配 profile ID（版本只有声明了某个 profile 才允许在该 profile 上注入）。 */
export type PromptProfileId =
  | 'chat_http'
  | 'responses_http'
  | 'chat_to_responses'
  | 'responses_to_chat'

/**
 * 运行期记录的固定原因枚举。
 *
 * applied 只表示“提示词已写入出站请求”，不表示任何行为变化。
 */
export type PromptReason =
  | 'applied'
  | 'disabled'
  | 'group_disabled'
  | 'account_disabled'
  | 'skipped_model_scope'
  | 'skipped_non_text_task'
  | 'unsupported_profile'
  | 'unsupported_history_mode'
  | 'no_compatible_account'
  | 'config_unavailable'
  | 'invalid_config'
  | 'adapter_error'

/** 策略来源。 */
export type PromptBindingSource = 'group' | 'account_override'

/** 模板元数据（不含正文）。 */
export interface PromptTemplate {
  id: number
  name: string
  description?: string
  source_url?: string
  source_note?: string
  archived_at?: string
  revision: number
  created_by?: number
  updated_by?: number
  created_at: string
  updated_at: string
}

/** 可编辑草稿（每模板一份）。 */
export interface PromptTemplateDraft {
  id: number
  template_id: number
  body: string
  client_models: string[]
  upstream_models: string[]
  supported_profiles: string[]
  revision: number
  updated_by?: number
  created_at: string
  updated_at: string
}

/** 不可变的已发布版本快照。 */
export interface PromptTemplateVersion {
  id: number
  template_id: number
  version_no: number
  body: string
  body_sha256: string
  body_bytes: number
  client_models: string[]
  upstream_models: string[]
  supported_profiles: string[]
  manifest_sha256: string
  change_note?: string
  published_by?: number
  published_at: string
  created_at: string
}

/**
 * 分组级绑定。
 *
 * 后端在“没有绑定行”时返回 mode=disabled、revision=0 的合成对象，
 * 因此 id / created_at / updated_at 可能缺失。
 */
export interface GroupPromptBinding {
  id?: number
  group_id: number
  mode: PromptBindingMode
  version_id?: number
  revision: number
  updated_by?: number
  created_at?: string
  updated_at?: string
}

/** 账号在某个分组内的覆盖。 */
export interface AccountGroupPromptOverride {
  id?: number
  account_id: number
  group_id: number
  mode: PromptOverrideMode
  version_id?: number
  revision: number
  updated_by?: number
  created_at?: string
  updated_at?: string
}

/** 管理操作审计事件（不含正文）。 */
export interface PromptAdminEvent {
  id: number
  action: string
  scope: string
  template_id?: number
  version_id?: number
  group_id?: number
  account_id?: number
  actor_id?: number
  actor_name?: string
  before_state?: Record<string, unknown>
  after_state?: Record<string, unknown>
  request_id?: string
  note?: string
  created_at: string
}

/** 发布前校验结果（不写库、不调用上游）。 */
export interface PromptValidationReport {
  valid: boolean
  errors: string[] | null
  body_bytes: number
  body_sha256: string
  manifest_sha256: string
  client_models: string[]
  supported_profiles: string[]
}

/** 单个 profile 的结构预览。 */
export interface PromptProfilePreview {
  profile: string
  supported: boolean
  field: string
  original_value?: string
  injected_value?: string
  error?: string
}

/**
 * 结构预览结果。
 *
 * 只做本地结构投影，不发送请求、不产生费用，也不预测模型行为。
 */
export interface PromptPreviewResult {
  version_id: number
  manifest_sha256: string
  body_bytes: number
  added_bytes: number
  client_models: string[]
  supported_profiles: string[]
  profile_previews: PromptProfilePreview[]
}

/** 请求级策略记录。 */
export interface PromptRequestEvent {
  id: number
  request_id?: string
  attempt_no: number
  group_id?: number
  account_id?: number
  client_model?: string
  upstream_model?: string
  outbound_profile?: string
  binding_source?: string
  version_id?: number
  manifest_sha256?: string
  applied: boolean
  reason: string
  added_bytes: number
  apply_duration_ms: number
  created_at: string
}

/** 模板列表查询参数。 */
export interface PromptTemplateListQuery {
  include_archived?: boolean
}

/** 创建模板请求。 */
export interface CreatePromptTemplatePayload {
  name: string
  description?: string
  source_url?: string
  source_note?: string
}

/** 更新模板元数据请求（必须携带 revision）。 */
export interface UpdatePromptTemplatePayload extends CreatePromptTemplatePayload {
  revision: number
}

/** 更新草稿请求。revision 为 0 表示首次写入。 */
export interface UpdatePromptDraftPayload {
  body: string
  client_models: string[]
  upstream_models: string[]
  supported_profiles: string[]
  revision: number
}

/** 发布版本请求。 */
export interface PublishPromptVersionPayload {
  draft_revision: number
  change_note?: string
  idempotency_key?: string
}

/** 分组绑定写入请求。 */
export interface SetPromptBindingPayload {
  mode: PromptBindingMode
  version_id?: number | null
  revision: number
}

/** 账号覆盖写入请求。 */
export interface SetPromptOverridePayload {
  mode: PromptOverrideMode
  version_id?: number | null
  revision: number
}

/**
 * 请求记录查询条件。
 *
 * 只传 limit 时后端返回事件数组（历史契约）；传 page / page_size 时返回分页信封。
 */
export interface PromptRequestEventQuery {
  request_id?: string
  group_id?: number
  version_id?: number
  applied?: boolean
  limit?: number
}

/**
 * 分页形态的请求记录查询条件。
 *
 * 后端只要看到 page 或 page_size 就切换到分页信封，此时 limit 被忽略。
 */
export interface PromptRequestEventPageQuery extends PromptRequestEventQuery {
  page: number
  page_size: number
}

/** 列表页用的模板 + 最新发布版本组合（版本由前端按需补充）。 */
export interface PromptTemplateListRow {
  template: PromptTemplate
  latestVersion: PromptTemplateVersion | null
}
