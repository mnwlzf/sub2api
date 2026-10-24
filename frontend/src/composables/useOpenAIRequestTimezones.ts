/**
 * OpenAI 请求时区列表的共享加载器。
 *
 * 列表来自 GET /admin/accounts/openai-request-timezones，一次会话内不会变化，
 * 因此模块级缓存只请求一次；创建/编辑账号弹窗共享同一份数据。
 */

import { computed, ref } from 'vue'
import { adminAPI } from '@/api/admin'
import {
  DEFAULT_OPENAI_REQUEST_TIMEZONE,
  buildOpenAIRequestTimezoneOptions
} from '@/components/account/openaiRequestTimezone'

const defaultTimezone = ref(DEFAULT_OPENAI_REQUEST_TIMEZONE)
const timezones = ref<string[]>([])
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)
let inflight: Promise<void> | null = null

export function useOpenAIRequestTimezones() {
  const load = async (force = false): Promise<void> => {
    if (inflight) return inflight
    if (loaded.value && !force) return
    loading.value = true
    error.value = null
    inflight = (async () => {
      try {
        const data = await adminAPI.accounts.getOpenAIRequestTimezones()
        const list = Array.isArray(data.timezones) ? data.timezones : []
        timezones.value = list
        defaultTimezone.value = (data.default || '').trim() || DEFAULT_OPENAI_REQUEST_TIMEZONE
        loaded.value = true
      } catch (err) {
        const message =
          typeof err === 'object' && err !== null && 'message' in err
            ? String((err as { message?: unknown }).message ?? '')
            : ''
        error.value = message || 'request_failed'
      } finally {
        loading.value = false
        inflight = null
      }
    })()
    return inflight
  }

  const options = computed(() =>
    buildOpenAIRequestTimezoneOptions(
      defaultTimezone.value,
      timezones.value,
      // 标签由调用方传入更灵活；这里用中英双语的「默认」占位，
      // 组件层会用自己的 i18n 文案覆盖第一项 label。
      'Default'
    )
  )

  return {
    defaultTimezone,
    timezones,
    loading,
    loaded,
    error,
    options,
    load
  }
}

/** 仅测试使用：重置模块级缓存。 */
export function __resetOpenAIRequestTimezonesForTests(): void {
  defaultTimezone.value = DEFAULT_OPENAI_REQUEST_TIMEZONE
  timezones.value = []
  loading.value = false
  loaded.value = false
  error.value = null
  inflight = null
}
