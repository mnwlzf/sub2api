/**
 * 文本提示词管理 API 客户端。
 *
 * 沿用仓库既有约定：apiClient 的响应拦截器已经把 { code, message, data }
 * 拆包成 data，因此这里直接返回 `data`；错误以 { status, code, reason, message }
 * 的普通对象 reject（409 冲突可用 status === 409 判断）。
 */

import { apiClient } from '@/api/client'
import type { PaginatedResponse } from '@/types'
import type {
  AccountGroupPromptOverride,
  CreatePromptTemplatePayload,
  GroupPromptBinding,
  PromptAdminEvent,
  PromptPreviewResult,
  PromptRequestEvent,
  PromptRequestEventPageQuery,
  PromptRequestEventQuery,
  PromptTemplate,
  PromptTemplateDraft,
  PromptTemplateVersion,
  PromptValidationReport,
  PublishPromptVersionPayload,
  SetPromptBindingPayload,
  SetPromptOverridePayload,
  UpdatePromptDraftPayload,
  UpdatePromptTemplatePayload,
} from './types'

const templatesPath = '/admin/prompt-templates'
const versionsPath = '/admin/prompt-versions'

// ---------------------------------------------------------------- 模板

export async function listTemplates(includeArchived = false): Promise<PromptTemplate[]> {
  const { data } = await apiClient.get<PromptTemplate[]>(templatesPath, {
    params: includeArchived ? { include_archived: true } : undefined,
  })
  return data
}

export async function createTemplate(payload: CreatePromptTemplatePayload): Promise<PromptTemplate> {
  const { data } = await apiClient.post<PromptTemplate>(templatesPath, payload)
  return data
}

export async function getTemplate(id: number): Promise<PromptTemplate> {
  const { data } = await apiClient.get<PromptTemplate>(`${templatesPath}/${id}`)
  return data
}

export async function updateTemplate(
  id: number,
  payload: UpdatePromptTemplatePayload,
): Promise<PromptTemplate> {
  const { data } = await apiClient.put<PromptTemplate>(`${templatesPath}/${id}`, payload)
  return data
}

export async function archiveTemplate(id: number, revision: number): Promise<void> {
  await apiClient.post(`${templatesPath}/${id}/archive`, { revision })
}

export async function listTemplateEvents(id: number): Promise<PromptAdminEvent[]> {
  const { data } = await apiClient.get<PromptAdminEvent[]>(`${templatesPath}/${id}/events`)
  return data
}

// ---------------------------------------------------------------- 草稿

export async function getDraft(id: number): Promise<PromptTemplateDraft> {
  const { data } = await apiClient.get<PromptTemplateDraft>(`${templatesPath}/${id}/draft`)
  return data
}

export async function updateDraft(
  id: number,
  payload: UpdatePromptDraftPayload,
): Promise<PromptTemplateDraft> {
  const { data } = await apiClient.put<PromptTemplateDraft>(`${templatesPath}/${id}/draft`, payload)
  return data
}

export async function getValidation(id: number): Promise<PromptValidationReport> {
  const { data } = await apiClient.get<PromptValidationReport>(`${templatesPath}/${id}/validation`)
  return data
}

// ---------------------------------------------------------------- 版本

export async function listVersions(id: number): Promise<PromptTemplateVersion[]> {
  const { data } = await apiClient.get<PromptTemplateVersion[]>(`${templatesPath}/${id}/versions`)
  return data
}

export async function publishVersion(
  id: number,
  payload: PublishPromptVersionPayload,
): Promise<PromptTemplateVersion> {
  const { data } = await apiClient.post<PromptTemplateVersion>(`${templatesPath}/${id}/versions`, payload)
  return data
}

export async function getVersion(id: number): Promise<PromptTemplateVersion> {
  const { data } = await apiClient.get<PromptTemplateVersion>(`${versionsPath}/${id}`)
  return data
}

export async function previewVersion(id: number): Promise<PromptPreviewResult> {
  const { data } = await apiClient.post<PromptPreviewResult>(`${versionsPath}/${id}/preview`)
  return data
}

// ---------------------------------------------------------------- 请求记录

/**
 * 查询运行期策略记录。
 *
 * 后端对同一端点保留两种契约（见 handler/admin/prompt_template_handler.go）：
 *   - 只传 limit（历史调用方式）→ 直接返回事件数组；
 *   - 传 page / page_size → 返回分页信封 { items, total, page, page_size, pages }。
 *
 * 这里用重载把两种形态都表达出来，调用方按是否传分页参数得到对应的返回类型。
 */
export function listRequestEvents(
  query: PromptRequestEventPageQuery,
): Promise<PaginatedResponse<PromptRequestEvent>>
export function listRequestEvents(query?: PromptRequestEventQuery): Promise<PromptRequestEvent[]>
export async function listRequestEvents(
  query: PromptRequestEventQuery | PromptRequestEventPageQuery = {},
): Promise<PromptRequestEvent[] | PaginatedResponse<PromptRequestEvent>> {
  const { data } = await apiClient.get<PromptRequestEvent[] | PaginatedResponse<PromptRequestEvent>>(
    '/admin/prompt-request-events',
    { params: query },
  )
  return data
}

// ---------------------------------------------------------------- 分组绑定

export async function getGroupBinding(groupId: number): Promise<GroupPromptBinding> {
  const { data } = await apiClient.get<GroupPromptBinding>(`/admin/groups/${groupId}/prompt-binding`)
  return data
}

export async function setGroupBinding(
  groupId: number,
  payload: SetPromptBindingPayload,
): Promise<GroupPromptBinding> {
  const { data } = await apiClient.put<GroupPromptBinding>(
    `/admin/groups/${groupId}/prompt-binding`,
    payload,
  )
  return data
}

export async function clearGroupBinding(groupId: number): Promise<void> {
  await apiClient.delete(`/admin/groups/${groupId}/prompt-binding`)
}

// ---------------------------------------------------------------- 账号覆盖

export async function listAccountOverrides(groupId: number): Promise<AccountGroupPromptOverride[]> {
  const { data } = await apiClient.get<AccountGroupPromptOverride[]>(
    `/admin/groups/${groupId}/prompt-overrides`,
  )
  return data
}

export async function getAccountOverride(
  groupId: number,
  accountId: number,
): Promise<AccountGroupPromptOverride> {
  const { data } = await apiClient.get<AccountGroupPromptOverride>(
    `/admin/groups/${groupId}/accounts/${accountId}/prompt-override`,
  )
  return data
}

export async function setAccountOverride(
  groupId: number,
  accountId: number,
  payload: SetPromptOverridePayload,
): Promise<AccountGroupPromptOverride> {
  const { data } = await apiClient.put<AccountGroupPromptOverride>(
    `/admin/groups/${groupId}/accounts/${accountId}/prompt-override`,
    payload,
  )
  return data
}

export async function clearAccountOverride(groupId: number, accountId: number): Promise<void> {
  await apiClient.delete(`/admin/groups/${groupId}/accounts/${accountId}/prompt-override`)
}

export const promptTemplatesAPI = {
  listTemplates,
  createTemplate,
  getTemplate,
  updateTemplate,
  archiveTemplate,
  listTemplateEvents,
  getDraft,
  updateDraft,
  getValidation,
  listVersions,
  publishVersion,
  getVersion,
  previewVersion,
  listRequestEvents,
  getGroupBinding,
  setGroupBinding,
  clearGroupBinding,
  listAccountOverrides,
  getAccountOverride,
  setAccountOverride,
  clearAccountOverride,
}

export default promptTemplatesAPI
