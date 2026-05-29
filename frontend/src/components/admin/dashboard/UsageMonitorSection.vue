<template>
  <div class="space-y-6">
    <div class="card p-4">
      <div class="mb-4 flex items-start justify-between gap-3">
        <div>
          <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.usageMonitor.title') }}</h3>
          <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.usageMonitor.description') }}</p>
        </div>
        <button type="button" class="btn btn-secondary btn-sm shrink-0" :disabled="loading" @click="loadData">
          <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
          {{ t('common.refresh') }}
        </button>
      </div>

      <div class="flex flex-wrap items-start gap-3">
        <div class="w-full sm:w-40">
          <Select v-model="granularity" :options="granularityOptions" @change="handleGranularityChange" />
        </div>

        <div ref="userDropdownRef" class="relative w-full sm:w-72">
          <Icon name="search" size="md" class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
          <input
            v-model="userKeyword"
            type="text"
            class="input pl-10 pr-8"
            :placeholder="t('admin.usageMonitor.userFilter')"
            @input="debounceSearchUsers"
            @focus="showUserDropdown = true"
          />
          <button
            v-if="selectedUsers.length"
            type="button"
            class="absolute right-2 top-1/2 -translate-y-1/2 text-gray-400 hover:text-gray-600 dark:hover:text-gray-300"
            @click="clearUser"
          >
            <Icon name="x" size="sm" />
          </button>

          <div
            v-if="showUserDropdown && (userResults.length > 0 || userKeyword)"
            class="absolute z-50 mt-1 max-h-60 w-full overflow-auto rounded-lg border border-gray-200 bg-white shadow-lg dark:border-gray-700 dark:bg-gray-800"
          >
            <div v-if="userLoading" class="px-4 py-3 text-sm text-gray-500">{{ t('common.loading') }}</div>
            <div v-else-if="userResults.length === 0 && userKeyword" class="px-4 py-3 text-sm text-gray-500">
              {{ t('common.noOptionsFound') }}
            </div>
            <button
              v-for="user in userResults"
              :key="user.id"
              type="button"
              class="w-full px-4 py-2 text-left text-sm hover:bg-gray-100 dark:hover:bg-gray-700"
              @click="selectUser(user)"
            >
              <span class="font-medium text-gray-900 dark:text-white">{{ user.email }}</span>
              <span class="ml-2 text-gray-500 dark:text-gray-400">#{{ user.id }}</span>
            </button>
          </div>
        </div>

        <div v-if="selectedUsers.length" class="flex w-full flex-wrap gap-2">
          <button
            v-for="user in selectedUsers"
            :key="user.id"
            type="button"
            class="inline-flex max-w-full items-center gap-1.5 rounded-full border border-primary-200 bg-primary-50 px-3 py-1 text-xs font-medium text-primary-700 hover:bg-primary-100 dark:border-primary-700/60 dark:bg-primary-500/10 dark:text-primary-300"
            @click="removeSelectedUser(user.id)"
          >
            <span class="max-w-[220px] truncate">{{ user.email }}</span>
            <Icon name="x" size="xs" />
          </button>
        </div>

        <div v-if="granularity !== 'hour' && granularity !== 'day'" class="w-full sm:w-auto">
          <DateRangePicker :start-date="startDate" :end-date="endDate" @change="handleRangeChange" />
        </div>

        <div class="w-full sm:w-40">
          <Select v-model="trendMetric" :options="trendMetricOptions" @change="loadData" />
        </div>

        <div class="ml-auto text-sm text-gray-500 dark:text-gray-400">
          {{ rangeHint }}
        </div>
      </div>
    </div>

    <div v-if="loading" class="card flex items-center justify-center py-16">
      <LoadingSpinner />
    </div>

    <template v-else>
      <div class="grid grid-cols-1 gap-6 xl:grid-cols-3">
        <div class="card p-4 xl:col-span-2">
          <div class="mb-4 flex items-center justify-between gap-3">
            <div>
              <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.usageMonitor.title') }}</h3>
              <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.usageMonitor.hoverHint') }}</p>
            </div>
            <button type="button" class="btn btn-secondary btn-xs shrink-0" :disabled="loading" @click="loadData">
              <Icon name="refresh" size="xs" :class="loading ? 'animate-spin' : ''" />
              {{ t('common.refresh') }}
            </button>
          </div>
          <div class="relative h-[420px]">
            <Line v-if="summaryTrendChartData" :data="summaryTrendChartData" :options="summaryTrendChartOptions" />
            <div v-else class="flex h-full items-center justify-center text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.usageMonitor.noData') }}
            </div>

            <div
              v-if="summaryTooltip.visible"
              class="pointer-events-none absolute z-20 w-[340px] max-w-[calc(100vw-2rem)] rounded-2xl border border-gray-700/70 bg-gray-950/95 px-3 py-2.5 text-xs text-gray-100 shadow-[0_18px_48px_-18px_rgba(15,23,42,0.75)] backdrop-blur-sm"
              :style="{
                left: `${summaryTooltip.x}px`,
                top: `${summaryTooltip.y}px`
              }"
            >
              <div class="text-[11px] font-semibold text-white">
                {{ summaryTooltip.title }}
              </div>
              <div class="mt-2 flex items-center gap-2">
                <span class="h-2.5 w-2.5 rounded-full bg-primary-500"></span>
                <span class="min-w-0 flex-1 truncate text-[11px] text-gray-200">{{ metricLabel }}</span>
                <span class="shrink-0 text-[11px] font-medium text-white">{{ formatMetricValue(summaryTooltip.totalValue) }}</span>
              </div>
              <div class="mt-2 border-t border-white/10 pt-2">
                <div class="mb-1 text-[10px] font-medium uppercase tracking-wide text-gray-400">{{ t('admin.usageMonitor.models') }}</div>
                <div class="space-y-1">
                  <div
                    v-for="model in summaryTooltip.models"
                    :key="model.model"
                    class="flex items-center justify-between gap-3"
                  >
                    <span class="min-w-0 flex-1 truncate text-[11px] text-gray-200">{{ model.model }}</span>
                    <span class="shrink-0 font-medium text-cyan-300">${{ formatCost(model.actual_cost) }}</span>
                  </div>
                  <div v-if="summaryTooltip.models.length === 0" class="text-[11px] text-gray-400">-</div>
                  <div v-if="summaryTooltip.remainingModels > 0" class="text-[11px] text-gray-400">
                    +{{ summaryTooltip.remainingModels }} more
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>

        <div class="card p-4">
          <div class="mb-3 flex items-center justify-between gap-3">
            <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.usageMonitor.topUsers') }}</h3>
            <button type="button" class="btn btn-secondary btn-xs shrink-0" :disabled="loading" @click="loadData">
              <Icon name="refresh" size="xs" :class="loading ? 'animate-spin' : ''" />
              {{ t('common.refresh') }}
            </button>
          </div>
          <div class="space-y-3">
            <div
              v-for="(user, index) in usageMonitorData?.top_users ?? []"
              :key="user.user_id"
              class="rounded-lg border p-3 transition-colors dark:border-gray-700"
              :class="hoveredUserId === user.user_id
                ? 'border-primary-400 bg-primary-50/70 dark:border-primary-500/60 dark:bg-primary-500/10'
                : 'border-gray-200 dark:border-gray-700'"
            >
              <div class="flex items-center justify-between gap-3">
                <div class="min-w-0">
                  <div class="truncate text-sm font-medium text-gray-900 dark:text-white">
                    {{ user.email || `User #${user.user_id}` }}
                  </div>
                  <div class="text-xs text-gray-500 dark:text-gray-400">#{{ index + 1 }} / {{ user.user_id }}</div>
                </div>
                <div class="shrink-0 text-right">
                  <div class="text-[11px] text-gray-500 dark:text-gray-400">{{ metricLabel }}</div>
                  <div class="text-sm font-semibold text-gray-900 dark:text-white">{{ formatMetricValue(topUserMetricTotal(user.user_id)) }}</div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div class="card p-4">
        <div class="mb-4 flex items-center justify-between gap-3">
          <div>
            <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.dashboard.recentUsage') }}</h3>
            <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.usageMonitor.models') }}</p>
          </div>
          <button type="button" class="btn btn-secondary btn-xs shrink-0" :disabled="loading" @click="loadData">
            <Icon name="refresh" size="xs" :class="loading ? 'animate-spin' : ''" />
            {{ t('common.refresh') }}
          </button>
        </div>
        <div class="relative h-[360px]">
          <Line v-if="topUserChartData" :data="topUserChartData" :options="topUserChartOptions" />
          <div v-else class="flex h-full items-center justify-center text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.usageMonitor.noData') }}
          </div>

          <div
            v-if="topUserTooltip.visible"
            class="pointer-events-none absolute z-20 w-[340px] max-w-[calc(100vw-2rem)] rounded-2xl border border-gray-700/70 bg-gray-950/95 px-3 py-2.5 text-xs text-gray-100 shadow-[0_18px_48px_-18px_rgba(15,23,42,0.75)] backdrop-blur-sm"
            :style="{
              left: `${topUserTooltip.x}px`,
              top: `${topUserTooltip.y}px`
            }"
          >
            <div class="text-[11px] font-semibold text-white">
              {{ topUserTooltip.title }}
            </div>
            <div class="mt-2 flex items-center gap-2">
              <span class="h-2.5 w-2.5 rounded-full" :style="{ backgroundColor: topUserTooltip.color }"></span>
              <span class="min-w-0 flex-1 truncate text-[11px] text-gray-200">{{ topUserTooltip.seriesLabel }}</span>
              <span class="shrink-0 text-[11px] font-medium text-white">{{ formatMetricValue(topUserTooltip.actualCost) }}</span>
            </div>
            <div class="mt-2 border-t border-white/10 pt-2">
              <div class="mb-1 text-[10px] font-medium uppercase tracking-wide text-gray-400">{{ t('admin.usageMonitor.models') }}</div>
              <div class="space-y-1">
                <div
                  v-for="model in topUserTooltip.models"
                  :key="model.model"
                  class="flex items-center justify-between gap-3"
                >
                  <span class="min-w-0 flex-1 truncate text-[11px] text-gray-200">{{ model.model }}</span>
                  <span class="shrink-0 font-medium text-cyan-300">${{ formatCost(model.actual_cost) }}</span>
                </div>
                <div v-if="topUserTooltip.remainingModels > 0" class="text-[11px] text-gray-400">
                  +{{ topUserTooltip.remainingModels }} more
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div class="card p-4">
        <div class="mb-4 flex items-center justify-between gap-3">
          <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.dashboard.spendingRankingTitle') }}</h3>
          <button type="button" class="btn btn-secondary btn-xs shrink-0" :disabled="loading" @click="loadData">
            <Icon name="refresh" size="xs" :class="loading ? 'animate-spin' : ''" />
            {{ t('common.refresh') }}
          </button>
        </div>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="text-left text-gray-500 dark:text-gray-400">
                <th class="pb-3">{{ t('admin.dashboard.spendingRankingUser') }}</th>
                <th class="pb-3 text-right">{{ t('admin.dashboard.spendingRankingRequests') }}</th>
                <th class="pb-3 text-right">{{ t('admin.dashboard.spendingRankingTokens') }}</th>
                <th class="pb-3 text-right">{{ t('admin.dashboard.spendingRankingSpend') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="item in rankingItems"
                :key="item.user_id"
                class="border-t border-gray-100 transition-colors dark:border-gray-700"
                :class="hoveredUserId === item.user_id ? 'bg-primary-50/70 dark:bg-primary-500/10' : ''"
              >
                <td class="py-3 text-gray-900 dark:text-white">{{ item.email || `User #${item.user_id}` }}</td>
                <td class="py-3 text-right text-gray-600 dark:text-gray-300">{{ formatNumber(item.requests) }}</td>
                <td class="py-3 text-right text-gray-600 dark:text-gray-300">{{ formatTokens(item.tokens) }}</td>
                <td class="py-3 text-right text-green-600 dark:text-green-400">${{ formatCost(item.actual_cost) }}</td>
              </tr>
              <tr v-if="rankingItems.length === 0">
                <td colspan="4" class="py-8 text-center text-sm text-gray-500 dark:text-gray-400">{{ t('admin.usageMonitor.noData') }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Chart as ChartJS, CategoryScale, LinearScale, LineElement, PointElement, Tooltip, Legend, Filler } from 'chart.js'
import { Line } from 'vue-chartjs'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import { adminAPI } from '@/api/admin'
import type { SimpleUser } from '@/api/admin/usage'
import type { UserSpendingRankingItem, UsageCostMonitorData } from '@/types'

ChartJS.register(CategoryScale, LinearScale, LineElement, PointElement, Tooltip, Legend, Filler)

const { t } = useI18n()

type Granularity = 'hour' | 'day' | 'week' | 'month'
type TrendMetric = 'requests' | 'tokens' | 'actual_cost'

const loading = ref(false)
const usageMonitorData = ref<UsageCostMonitorData | null>(null)
const rankingItems = ref<UserSpendingRankingItem[]>([])

const granularity = ref<Granularity>('hour')
const trendMetric = ref<TrendMetric>('actual_cost')
const startDate = ref('')
const endDate = ref('')

const userKeyword = ref('')
const userResults = ref<SimpleUser[]>([])
const userLoading = ref(false)
const showUserDropdown = ref(false)
const selectedUsers = ref<SimpleUser[]>([])
const userDropdownRef = ref<HTMLElement | null>(null)
let userSearchTimer: number | undefined
const hoveredUserId = ref<number | null>(null)
const summaryTooltip = ref({
  visible: false,
  x: 0,
  y: 0,
  title: '',
  totalValue: 0,
  models: [] as { model: string; actual_cost: number }[],
  remainingModels: 0,
})
const topUserTooltip = ref({
  visible: false,
  x: 0,
  y: 0,
  placement: 'bottom' as 'bottom' | 'top',
  title: '',
  seriesLabel: '',
  actualCost: 0,
  color: '#2563eb',
  models: [] as { model: string; actual_cost: number }[],
  remainingModels: 0,
})

const granularityOptions = computed(() => ([
  { value: 'hour', label: t('admin.usageMonitor.hour') },
  { value: 'day', label: t('admin.usageMonitor.day') },
  { value: 'week', label: t('admin.usageMonitor.week') },
  { value: 'month', label: t('admin.usageMonitor.month') }
]))

const trendMetricOptions = computed(() => ([
  { value: 'actual_cost', label: t('admin.usageMonitor.actualCost') },
  { value: 'tokens', label: t('admin.dashboard.tokens') },
  { value: 'requests', label: t('admin.dashboard.requests') }
]))

const rangeHint = computed(() => {
  if (granularity.value === 'hour') return t('admin.usageMonitor.lastHour')
  if (granularity.value === 'day') return t('admin.usageMonitor.last24h')
  return t('admin.usageMonitor.limitedRange')
})

const HOUR_MS = 60 * 60 * 1000
const DAY_WINDOW_HOURS = 24

const userTimezone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
const formatDate = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
const parseBucketDate = (bucket: string): Date | null => {
  const normalized = bucket.includes(' ') ? bucket.replace(' ', 'T') : bucket
  const timestamp = Date.parse(normalized)
  return Number.isNaN(timestamp) ? null : new Date(timestamp)
}
const parseBucketTime = (bucket: string): number => parseBucketDate(bucket)?.getTime() ?? 0
const sortBuckets = (buckets: string[]) => [...buckets].sort((a, b) => parseBucketTime(a) - parseBucketTime(b))
const addHours = (date: Date, hours: number) => new Date(date.getTime() + hours * HOUR_MS)
const nextHour = (date = new Date()) => {
  const end = new Date(date)
  end.setMinutes(0, 0, 0)
  end.setHours(end.getHours() + 1)
  return end
}
const nextMinute = (date = new Date()) => {
  const end = new Date(date)
  end.setSeconds(0, 0)
  end.setMinutes(end.getMinutes() + 1)
  return end
}
const formatHourLabel = (bucket: string, offsetHours = 0) => {
  const parsed = parseBucketDate(bucket)
  if (!parsed) return bucket.includes(' ') ? bucket.slice(11, 16) : bucket
  const displayTime = offsetHours ? addHours(parsed, offsetHours) : parsed
  return `${String(displayTime.getHours()).padStart(2, '0')}:00`
}
const formatMinuteLabel = (bucket: string) => {
  const parsed = parseBucketDate(bucket)
  if (!parsed) return bucket.includes(' ') ? bucket.slice(11, 16) : bucket
  return `${String(parsed.getHours()).padStart(2, '0')}:${String(parsed.getMinutes()).padStart(2, '0')}`
}
const formatHourTooltipLabel = (bucket: string, offsetHours = 0) => {
  const parsed = parseBucketDate(bucket)
  if (!parsed) return bucket
  const displayTime = offsetHours ? addHours(parsed, offsetHours) : parsed
  return `${formatDate(displayTime)} ${String(displayTime.getHours()).padStart(2, '0')}:00`
}
const formatMinuteTooltipLabel = (bucket: string) => {
  const parsed = parseBucketDate(bucket)
  if (!parsed) return bucket
  return `${formatDate(parsed)} ${String(parsed.getHours()).padStart(2, '0')}:${String(parsed.getMinutes()).padStart(2, '0')}`
}
const currentHourWindow = () => {
  const end = nextMinute()
  return {
    start: new Date(end.getTime() - HOUR_MS),
    end
  }
}
const currentDayWindow = () => {
  const end = nextHour()
  return {
    start: new Date(end.getTime() - DAY_WINDOW_HOURS * HOUR_MS),
    end
  }
}
const initHourRange = () => {
  const { start, end } = currentHourWindow()
  startDate.value = formatDate(start)
  endDate.value = formatDate(end)
}

const initRange = () => {
  const end = new Date()
  const start = new Date()
  start.setDate(start.getDate() - 30)
  startDate.value = formatDate(start)
  endDate.value = formatDate(end)
}
const initDayRange = () => {
  const { start, end } = currentDayWindow()
  startDate.value = formatDate(start)
  endDate.value = formatDate(end)
}

const formatNumber = (value: number) => value.toLocaleString()
const formatTokens = (value: number) => {
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(2)}B`
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(2)}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(2)}K`
  return value.toLocaleString()
}
const formatCost = (value: number) => {
  if (value >= 1000) return (value / 1000).toFixed(2) + 'K'
  if (value >= 1) return value.toFixed(2)
  if (value >= 0.01) return value.toFixed(3)
  return value.toFixed(4)
}

const formatTopUserTooltipTitle = (bucket: string) => {
  const parsed = parseBucketDate(bucket)
  if (!parsed) return `时间 ${bucket}`
  if (granularity.value === 'hour') return `时间 ${formatMinuteTooltipLabel(bucket)}`
  if (granularity.value === 'day') return `时间 ${formatHourTooltipLabel(bucket, 1)}`
  return `时间 ${formatDate(parsed)}`
}

const placeTooltipInChart = (caretX: number, caretY: number, width: number, height: number, chartWidth: number, chartHeight: number) => {
  const placeLeft = caretX + width + 24 > chartWidth
  const placeTop = caretY + height + 24 > chartHeight
  const x = placeLeft ? caretX - width - 16 : caretX + 16
  const y = placeTop ? caretY - height - 16 : caretY + 16
  return {
    x: Math.min(Math.max(12, x), Math.max(12, chartWidth - width - 12)),
    y: Math.min(Math.max(12, y), Math.max(12, chartHeight - height - 12)),
  }
}

const getBucketModelBreakdown = (bucket: string, limit = 6) => {
  const totals = new Map<string, number>()
  for (const point of usageMonitorData.value?.series || []) {
    if (point.bucket !== bucket) continue
    for (const model of point.models || []) {
      totals.set(model.model, (totals.get(model.model) || 0) + model.actual_cost)
    }
  }
  const models = Array.from(totals.entries())
    .map(([model, actual_cost]) => ({ model, actual_cost }))
    .sort((a, b) => b.actual_cost - a.actual_cost)
  return {
    models: models.slice(0, limit),
    remainingModels: Math.max(models.length - limit, 0),
  }
}

const updateSummaryTooltip = (chartTooltip: any, chartWidth: number, chartHeight: number) => {
  if (!chartTooltip || chartTooltip.opacity === 0 || !chartTooltip.dataPoints?.length) {
    summaryTooltip.value.visible = false
    return
  }

  const point = chartTooltip.dataPoints[0]
  const bucket = topUserBucketKeys.value[point?.dataIndex ?? -1] || String(point?.label || '')
  const { models, remainingModels } = getBucketModelBreakdown(bucket)
  const width = 340
  const height = 116 + models.length * 22 + (remainingModels > 0 ? 18 : 0)
  const position = placeTooltipInChart(
    Number(chartTooltip.caretX ?? 0),
    Number(chartTooltip.caretY ?? 0),
    width,
    height,
    chartWidth,
    chartHeight
  )

  summaryTooltip.value = {
    visible: true,
    x: position.x,
    y: position.y,
    title: formatTopUserTooltipTitle(bucket),
    totalValue: Number(point?.raw ?? 0),
    models,
    remainingModels,
  }
}

const updateTopUserTooltip = (chartTooltip: any, chartWidth: number, chartHeight: number) => {
  if (!chartTooltip || chartTooltip.opacity === 0 || !chartTooltip.dataPoints?.length) {
    hoveredUserId.value = null
    topUserTooltip.value.visible = false
    return
  }

  const point = chartTooltip.dataPoints[0]
  const userId = Number(point?.dataset?.userId ?? 0)
  hoveredUserId.value = userId || null
  const bucket = topUserBucketKeys.value[point?.dataIndex ?? -1] || String(point?.label || '')
  const record = pointLookup.value.get(`${userId}:${bucket}`)
  const models = (record?.models || []).slice(0, 4)
  const remainingModels = Math.max((record?.models?.length || 0) - models.length, 0)
  const width = 340
  const height = 116 + models.length * 22 + (remainingModels > 0 ? 18 : 0)
  const position = placeTooltipInChart(
    Number(chartTooltip.caretX ?? 0),
    Number(chartTooltip.caretY ?? 0),
    width,
    height,
    chartWidth,
    chartHeight
  )

  topUserTooltip.value = {
    visible: true,
    x: position.x,
    y: position.y,
    placement: 'bottom',
    title: formatTopUserTooltipTitle(bucket),
    seriesLabel: String(point?.dataset?.label || ''),
    actualCost: record?.metric_value ?? Number(point?.raw ?? 0),
    color: String(point?.dataset?.borderColor || '#2563eb'),
    models,
    remainingModels,
  }
}

const summaryTrendChartData = computed(() => {
  if (!usageMonitorData.value?.series.length) return null
  const buckets = topUserBucketKeys.value
  if (!buckets.length) return null

  const totalsByBucket = new Map<string, number>()
  for (const item of usageMonitorData.value.series) {
    const value = trendMetric.value === 'requests'
      ? item.requests
      : trendMetric.value === 'tokens'
        ? item.tokens
        : item.actual_cost
    totalsByBucket.set(item.bucket, (totalsByBucket.get(item.bucket) || 0) + value)
  }
  const data = buckets.map(bucket => totalsByBucket.get(bucket) || 0)
  if (!data.some(value => value > 0)) return null

  const labels = buckets.map(bucket => granularity.value === 'hour'
    ? formatMinuteLabel(bucket)
    : granularity.value === 'day'
      ? formatHourLabel(bucket, 1)
      : bucket)
  return {
    labels,
    datasets: [
      {
        label: trendMetric.value === 'requests' ? t('admin.dashboard.requests') : trendMetric.value === 'tokens' ? t('admin.dashboard.tokens') : t('admin.usageMonitor.actualCost'),
        data,
        borderColor: '#2563eb',
        backgroundColor: 'rgba(37,99,235,0.15)',
        fill: true,
        tension: 0.25,
        pointRadius: 2,
        pointHoverRadius: 6,
        pointHitRadius: 12
      }
    ]
  }
})

const metricValueFromPoint = (point: UsageCostMonitorData['series'][number]) => {
  if (trendMetric.value === 'requests') return point.requests
  if (trendMetric.value === 'tokens') return point.tokens
  return point.actual_cost
}

const formatMetricValue = (value: number) => {
  if (trendMetric.value === 'requests') return formatNumber(value)
  if (trendMetric.value === 'tokens') return formatTokens(value)
  return `$${formatCost(value)}`
}

const metricLabel = computed(() => (
  trendMetric.value === 'requests'
    ? t('admin.dashboard.requests')
    : trendMetric.value === 'tokens'
      ? t('admin.dashboard.tokens')
      : t('admin.usageMonitor.actualCost')
))

const topUserMetricTotal = (userId: number) => {
  if (trendMetric.value === 'actual_cost') {
    return usageMonitorData.value?.top_users.find(user => user.user_id === userId)?.total_actual_cost || 0
  }
  return (usageMonitorData.value?.series || []).reduce((total, point) => (
    point.user_id === userId ? total + metricValueFromPoint(point) : total
  ), 0)
}

const pointLookup = computed(() => {
  const map = new Map<string, { actual_cost: number; requests: number; tokens: number; metric_value: number; models: { model: string; actual_cost: number }[] }>()
  for (const item of usageMonitorData.value?.series ?? []) {
    map.set(`${item.user_id}:${item.bucket}`, {
      actual_cost: item.actual_cost,
      requests: item.requests,
      tokens: item.tokens,
      metric_value: metricValueFromPoint(item),
      models: item.models
    })
  }
  return map
})

const topUserChartData = computed(() => {
  if (!usageMonitorData.value?.series.length) return null
  const buckets = topUserBucketKeys.value
  const palette = ['#2563eb', '#16a34a', '#d97706', '#dc2626', '#7c3aed', '#0891b2', '#be123c', '#4d7c0f', '#9333ea', '#0f766e']
  return {
    labels: buckets.map(label => granularity.value === 'hour'
      ? formatMinuteLabel(label)
      : granularity.value === 'day'
        ? formatHourLabel(label, 1)
        : label),
    datasets: usageMonitorData.value.top_users.map((user, index) => {
      const userMap = new Map(
        usageMonitorData.value!.series
          .filter(item => item.user_id === user.user_id)
          .map(item => [item.bucket, metricValueFromPoint(item)])
      )
      return {
        label: user.email || `User #${user.user_id}`,
        userId: user.user_id,
        data: buckets.map(label => userMap.get(label) ?? 0),
        borderColor: palette[index % palette.length],
        backgroundColor: `${palette[index % palette.length]}22`,
        fill: false,
        tension: 0.25,
        pointRadius: 2,
        pointHoverRadius: 6,
        pointHitRadius: 12
      }
    })
  }
})

const topUserBucketKeys = computed(() => {
  if (!usageMonitorData.value?.series.length) return []
  return sortBuckets(Array.from(new Set(usageMonitorData.value.series.map(item => item.bucket))))
})

const mergeUsageMonitorResponses = (responses: UsageCostMonitorData[]) => {
  const topUsers = new Map<number, { user_id: number; email: string; total_actual_cost: number; requests: number; tokens: number }>()
  const series: UsageCostMonitorData['series'] = []

  for (const data of responses) {
    for (const user of data.top_users || []) {
      const existing = topUsers.get(user.user_id)
      if (!existing) {
        topUsers.set(user.user_id, { ...user })
      } else {
        existing.total_actual_cost += user.total_actual_cost
        existing.requests += user.requests
        existing.tokens += user.tokens
      }
    }
    series.push(...(data.series || []))
  }

  return {
    top_users: Array.from(topUsers.values()).sort((a, b) => {
      const left = trendMetric.value === 'requests' ? a.requests : trendMetric.value === 'tokens' ? a.tokens : a.total_actual_cost
      const right = trendMetric.value === 'requests' ? b.requests : trendMetric.value === 'tokens' ? b.tokens : b.total_actual_cost
      return right - left
    }),
    series,
  }
}

const buildRankingFromMonitorData = (data: UsageCostMonitorData | null): UserSpendingRankingItem[] => {
  if (!data?.top_users?.length) return []
  return data.top_users.map(user => ({
    user_id: user.user_id,
    email: user.email,
    actual_cost: user.total_actual_cost,
    requests: user.requests,
    tokens: user.tokens,
  }))
}

const summaryTrendChartOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  interaction: { intersect: false, mode: 'index' as const, axis: 'x' as const },
  plugins: {
    legend: { display: false },
    tooltip: {
      enabled: false,
      external: (context: any) => {
        const chart = context.chart
        updateSummaryTooltip(context.tooltip, chart.width, chart.height)
      }
    }
  },
  scales: {
    x: {
      ticks: {
        autoSkip: true,
        autoSkipPadding: 32,
        maxRotation: 0,
        minRotation: 0,
        maxTicksLimit: granularity.value === 'day' ? 5 : 8,
        callback: function (this: any, value: string | number) {
          return this.getLabelForValue(Number(value))
        }
      }
    },
    y: {
      ticks: {
        callback: (value: string | number) => {
          return formatMetricValue(Number(value))
        }
      }
    }
  }
}))

const topUserChartOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  interaction: { intersect: false, mode: 'nearest' as const, axis: 'xy' as const },
  plugins: {
    legend: { position: 'top' as const, labels: { usePointStyle: true, pointStyle: 'circle' } },
    tooltip: {
      enabled: false,
      external: (context: any) => {
        const chart = context.chart
        updateTopUserTooltip(context.tooltip, chart.width, chart.height)
      }
    }
  },
  scales: {
    x: {
      ticks: {
        autoSkip: true,
        autoSkipPadding: 32,
        maxRotation: 0,
        minRotation: 0,
        maxTicksLimit: granularity.value === 'day' ? 5 : 8,
        callback: function (this: any, value: string | number) {
          return this.getLabelForValue(Number(value))
        }
      }
    },
    y: { ticks: { callback: (value: string | number) => formatMetricValue(Number(value)) } }
  }
}))

const loadData = async () => {
  loading.value = true
  try {
    const monitorParams = {
      granularity: granularity.value,
      start_date: startDate.value,
      end_date: endDate.value,
      timezone: userTimezone(),
      limit: selectedUsers.value.length ? Math.max(selectedUsers.value.length, 1) : 8,
    }
    const monitorPromise = selectedUsers.value.length
      ? Promise.all(selectedUsers.value.map(user =>
          adminAPI.dashboard.getUsageCostMonitor({
            ...monitorParams,
            user_id: user.id,
            limit: 1,
          })
        )).then(responses => ({ data: mergeUsageMonitorResponses(responses.map(response => response.data)) }))
      : adminAPI.dashboard.getUsageCostMonitor(monitorParams)

    const [rankingRes, monitorRes] = await Promise.all([
      adminAPI.dashboard.getUserSpendingRanking({
        start_date: startDate.value,
        end_date: endDate.value,
        limit: 10
      }),
      monitorPromise
    ])
    usageMonitorData.value = monitorRes.data
    rankingItems.value = buildRankingFromMonitorData(monitorRes.data)
    if (!rankingItems.value.length) {
      rankingItems.value = rankingRes.ranking
    }
  } finally {
    loading.value = false
  }
}

const debounceSearchUsers = () => {
  if (userSearchTimer) window.clearTimeout(userSearchTimer)
  userSearchTimer = window.setTimeout(searchUsers, 300)
}

const searchUsers = async () => {
  const keyword = userKeyword.value.trim()
  if (!keyword) {
    userResults.value = []
    return
  }
  userLoading.value = true
  try {
    userResults.value = await adminAPI.usage.searchUsers(keyword)
  } finally {
    userLoading.value = false
  }
}

const selectUser = (user: SimpleUser) => {
  if (!selectedUsers.value.some(item => item.id === user.id)) {
    selectedUsers.value = [...selectedUsers.value, user].slice(0, 10)
  }
  userKeyword.value = ''
  userResults.value = []
  showUserDropdown.value = false
  loadData()
}

const removeSelectedUser = (userID: number) => {
  selectedUsers.value = selectedUsers.value.filter(user => user.id !== userID)
  loadData()
}

const clearUser = () => {
  selectedUsers.value = []
  userKeyword.value = ''
  userResults.value = []
  showUserDropdown.value = false
  loadData()
}

const handleGranularityChange = async () => {
  if (granularity.value === 'hour') {
    initHourRange()
  } else if (granularity.value === 'day') {
    initDayRange()
  } else {
    initRange()
  }
  await loadData()
}

const handleRangeChange = (range: { startDate: string; endDate: string }) => {
  startDate.value = range.startDate
  endDate.value = range.endDate
  loadData()
}

const handleClickOutside = (event: MouseEvent) => {
  const target = event.target as Node | null
  if (userDropdownRef.value && target && !userDropdownRef.value.contains(target)) {
    showUserDropdown.value = false
  }
}

onMounted(async () => {
  initHourRange()
  await loadData()
  document.addEventListener('click', handleClickOutside)
})

onUnmounted(() => {
  document.removeEventListener('click', handleClickOutside)
})
</script>
