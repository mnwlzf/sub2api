/**
 * 草稿编辑表单 <-> API 载荷 的纯函数转换。
 *
 * 表单把模型列表保留为多行文本，保存时再解析成数组，
 * 这样编辑过程中不会因为逐字符输入而反复丢弃中间状态。
 */

import { formatStringList, parseStringList } from './viewModel'
import type { PromptTemplateDraft, UpdatePromptDraftPayload } from './types'

export interface PromptDraftForm {
  body: string
  clientModelsText: string
  upstreamModelsText: string
  supportedProfiles: string[]
}

/** 服务器草稿 → 可编辑表单。 */
export function draftToForm(draft: PromptTemplateDraft | null): PromptDraftForm {
  return {
    body: draft?.body ?? '',
    clientModelsText: formatStringList(draft?.client_models),
    upstreamModelsText: formatStringList(draft?.upstream_models),
    supportedProfiles: [...(draft?.supported_profiles ?? [])].sort(),
  }
}

/** 表单 → 保存草稿的载荷（列表已去重、去空行）。 */
export function formToPayload(form: PromptDraftForm, revision: number): UpdatePromptDraftPayload {
  return {
    body: form.body,
    client_models: parseStringList(form.clientModelsText),
    upstream_models: parseStringList(form.upstreamModelsText),
    supported_profiles: [...form.supportedProfiles],
    revision,
  }
}

/** 用于“是否有未保存修改”的比较指纹。 */
export function formFingerprint(form: PromptDraftForm | null): string {
  if (!form) return ''
  return JSON.stringify({
    body: form.body,
    client_models: parseStringList(form.clientModelsText),
    upstream_models: parseStringList(form.upstreamModelsText),
    supported_profiles: [...form.supportedProfiles].sort(),
  })
}
