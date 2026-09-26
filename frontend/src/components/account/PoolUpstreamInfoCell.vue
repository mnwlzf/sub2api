<template>
  <div v-if="configured" class="flex h-6 min-w-[9rem] items-center gap-1">
    <HelpTooltip class="-ml-1" width-class="w-max max-w-[calc(100vw-2rem)]" data-testid="pool-upstream-info-details">
      <template #trigger>
        <span
          class="cursor-help border-b border-dotted border-gray-300 text-sm font-medium dark:border-dark-600"
          :class="hasValue ? 'font-mono text-gray-800 dark:text-gray-200' : statusClass || 'text-gray-400 dark:text-gray-500'"
          data-testid="pool-upstream-info"
        >
          {{ primaryValue }}
        </span>
      </template>
      <div class="space-y-1">
        <template v-if="dataLines.length">
          <p v-for="line in dataLines" :key="line" data-testid="pool-upstream-info-line">{{ line }}</p>
        </template>
        <p v-else>{{ statusLabel || '-' }}</p>
        <p v-if="snapshot?.received_at" data-testid="pool-upstream-info-received">
          {{ t('admin.accounts.poolUpstream.updatedAt', { value: formatDate(snapshot.received_at) }) }}
        </p>
        <p v-if="nextProbeAt" data-testid="pool-upstream-info-next-probe">
          {{ t('admin.accounts.poolUpstream.nextProbeAt', { value: formatDate(nextProbeAt) }) }}
        </p>
        <p v-if="snapshot?.last_error" class="text-red-300" data-testid="pool-upstream-info-error">
          {{ errorLabel }}
        </p>
      </div>
    </HelpTooltip>
    <span v-if="hasValue && statusLabel" :class="statusClass" class="whitespace-nowrap text-[10px] font-medium">
      {{ statusLabel }}
    </span>
    <button
      type="button"
      class="inline-flex h-6 w-6 flex-shrink-0 items-center justify-center rounded text-blue-600 transition-colors hover:bg-blue-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-blue-400 dark:hover:bg-blue-900/30"
      :disabled="probing"
      :aria-label="t('admin.accounts.poolUpstream.manualRefresh')"
      :title="t('admin.accounts.poolUpstream.manualRefresh')"
      data-testid="pool-upstream-info-probe"
      @click="$emit('probe')"
    >
      <Icon name="refresh" size="xs" :class="{ 'animate-spin': probing }" />
    </button>
  </div>
  <span v-else class="text-sm text-gray-400 dark:text-dark-500" data-testid="pool-upstream-info-empty">-</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Account, PoolUpstreamInfoSnapshot } from '@/types'

const props = withDefaults(defineProps<{
  account: Account
  now: number
  probing?: boolean
}>(), {
  probing: false
})

defineEmits<{
  (event: 'probe'): void
}>()

const { t } = useI18n()
const CLOCK_SKEW_TOLERANCE_MS = 5 * 60 * 1000

const selectionPlatform = computed(() => {
  const value = props.account.extra?.pool_upstream_platform
  return value === 'sub2api' || value === 'chatgpt2api' ? value : ''
})
const selectionFeatures = computed(() => {
  const raw = props.account.extra?.pool_upstream_features
  return Array.isArray(raw) ? raw.filter((item): item is string => typeof item === 'string') : []
})
// A disabled pool mode can never surface stale selected info: the admin list
// response keeps the non-sensitive credentials.pool_mode flag.
const configured = computed(() =>
  props.account.type === 'apikey' &&
  props.account.credentials?.pool_mode === true &&
  selectionPlatform.value !== '' &&
  selectionFeatures.value.length > 0
)

const snapshot = computed<PoolUpstreamInfoSnapshot | undefined>(() => props.account.extra?.pool_upstream_info)
// Unsupported upstreams do not carry a meaningful last value.
const data = computed(() => (snapshot.value?.status === 'unsupported' ? undefined : snapshot.value?.data))

const nextProbeAt = computed(() => {
  const value = snapshot.value?.next_probe_at
  return typeof value === 'string' && Number.isFinite(Date.parse(value)) ? value : ''
})
const receivedAt = computed(() =>
  typeof snapshot.value?.received_at === 'string' ? Date.parse(snapshot.value.received_at) : Number.NaN
)
const freshUntil = computed(() =>
  typeof snapshot.value?.fresh_until === 'string' ? Date.parse(snapshot.value.fresh_until) : Number.NaN
)
const stale = computed(() => {
  if (!snapshot.value) return false
  if (!Number.isFinite(receivedAt.value)) return snapshot.value.status === 'ok'
  if (receivedAt.value > props.now + CLOCK_SKEW_TOLERANCE_MS) return true
  if (!Number.isFinite(freshUntil.value) || freshUntil.value <= receivedAt.value) return true
  return props.now > freshUntil.value
})

const formatUsd = (value: number) =>
  Number.isFinite(value) ? `$${value.toFixed(2)}` : '-'
const formatCount = (value: number) =>
  Number.isFinite(value) ? String(Math.trunc(value)) : '-'

// Labeled value lines. accounts.active is a local "normal accounts" count and
// total_quota is a known image quota — neither is money nor a remotely
// confirmed availability signal. Only the wallet/quota and the two selected
// counters may appear in the cell itself; the optional breakdown counters stay
// tooltip-only.
const dataLineEntries = computed(() => {
  const info = data.value
  if (!info) return [] as { text: string; primary: boolean }[]
  const lines: { text: string; primary: boolean }[] = []
  if (typeof info.amount_usd === 'number') {
    const kind = info.kind === 'wallet' ? 'wallet' : info.kind === 'subscription' ? 'subscription' : 'quota'
    lines.push({ text: t(`admin.accounts.poolUpstream.amount_${kind}`, { value: formatUsd(info.amount_usd) }), primary: true })
  }
  if (typeof info.accounts_active === 'number') {
    lines.push({ text: t('admin.accounts.poolUpstream.accountsActive', { value: formatCount(info.accounts_active) }), primary: true })
  }
  if (typeof info.total_quota === 'number') {
    lines.push({ text: t('admin.accounts.poolUpstream.totalQuota', { value: formatCount(info.total_quota) }), primary: true })
  }
  if (typeof info.unlimited_quota_count === 'number') {
    lines.push({ text: t('admin.accounts.poolUpstream.unlimitedQuota', { value: formatCount(info.unlimited_quota_count) }), primary: false })
  }
  if (typeof info.unknown_quota_count === 'number') {
    lines.push({ text: t('admin.accounts.poolUpstream.unknownQuota', { value: formatCount(info.unknown_quota_count) }), primary: false })
  }
  return lines
})
const dataLines = computed(() => dataLineEntries.value.map(line => line.text))

// Compact primary text reuses the labeled lines, capped to the first two.
const primaryValue = computed(() => {
  const primaries = dataLineEntries.value.filter(line => line.primary).map(line => line.text)
  if (primaries.length > 0) {
    return primaries.slice(0, 2).join(' · ')
  }
  return statusLabel.value || '-'
})

const statusLabel = computed(() => {
  if (!snapshot.value) return t('admin.accounts.poolUpstream.notProbed')
  if (snapshot.value.status === 'unsupported') return t('admin.accounts.poolUpstream.unsupported')
  if (snapshot.value.status === 'failed') return t('admin.accounts.poolUpstream.failed')
  if (stale.value) return t('admin.accounts.poolUpstream.stale')
  return ''
})
const statusClass = computed(() => {
  if (!snapshot.value) return 'text-gray-400 dark:text-gray-500'
  if (snapshot.value.status === 'unsupported') return 'text-gray-500 dark:text-gray-400'
  if (snapshot.value.status === 'failed') return 'text-red-600 dark:text-red-400'
  if (stale.value) return 'text-amber-600 dark:text-amber-400'
  return ''
})
const hasValue = computed(() => dataLines.value.length > 0)
const errorLabel = computed(() => {
  const reason = snapshot.value?.last_error
  if (!reason) return ''
  const key = `admin.accounts.poolUpstream.errors.${reason}`
  const translated = t(key)
  return translated === key ? reason : translated
})
const formatDate = (value?: string) => value
  ? new Date(value).toLocaleString(undefined, {
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit'
    })
  : '-'
</script>
