<template>
  <span
    v-if="meta"
    :class="[
      'inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] font-medium leading-4',
      meta.badgeClass
    ]"
    :title="tooltip"
    data-testid="bps-credential-status-badge"
    :data-status="meta.status"
  >
    <span :class="['h-1.5 w-1.5 flex-shrink-0 rounded-full', meta.dotClass]" />
    <span>{{ t(`admin.accounts.bps.credentialStatus.${meta.labelKey}`) }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { OpenAIBPSCredentialState } from '@/types'
import { formatDateTime } from '@/utils/format'
import { openAIBPSCredentialStatusDetails, openAIBPSCredentialStatusMeta } from '@/utils/openaiBPS'

/**
 * OpenAI BPS 凭证状态徽标。
 *
 * 只对 openai_bps 平台且有 bps_credential_state 的账号渲染；tooltip 展示
 * 到期时间、最近异常时间与上游错误码，便于管理员定位凭证问题。
 */
const props = defineProps<{
  platform?: string | null
  state?: OpenAIBPSCredentialState | null
}>()

const { t } = useI18n()

const meta = computed(() => openAIBPSCredentialStatusMeta(props.platform, props.state))

const tooltip = computed(() => {
  if (!meta.value) return ''
  const details = openAIBPSCredentialStatusDetails(props.state)
  const lines = [t(`admin.accounts.bps.credentialStatus.${meta.value.labelKey}`)]
  if (details.expiresAt) {
    lines.push(t('admin.accounts.bps.credentialStatus.expiresAt', { time: formatDateTime(details.expiresAt) }))
  }
  if (details.lastSeenAt) {
    lines.push(t('admin.accounts.bps.credentialStatus.lastSeenAt', { time: formatDateTime(details.lastSeenAt) }))
  }
  if (details.errorCode) {
    lines.push(t('admin.accounts.bps.credentialStatus.errorCode', { code: details.errorCode }))
  }
  return lines.join('\n')
})
</script>
