<template>
  <div class="space-y-6">
    <div class="card p-4">
      <div class="mb-4">
        <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.usageMonitor.title') }}</h3>
        <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.usageMonitor.description') }}</p>
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
            v-if="selectedUser"
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

        <div v-if="granularity !== 'day'" class="w-full sm:w-auto">
          <DateRangePicker :start-date="startDate" :end-date="endDate" @change="handleRangeChange" />
        </div>

        <div class="w-full sm:w-40">
          <Select v-model="trendMetric" :options="trendMetricOptions" @change="loadData" />
        </div>

        <div class="ml-auto text-sm text-gray-500 dark:text-gray-400">
          {{ granularity === 'day' ? t('admin.usageMonitor.last24h') : t('admin.usageMonitor.limitedRange') }}
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
          </div>
          <div class="h-[420px]">
            <Line v-if="summaryTrendChartData" :data="summaryTrendChartData" :options="summaryTrendChartOptions" />
            <div v-else class="flex h-full items-center justify-center text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.usageMonitor.noData') }}
            </div>
          </div>
        </div>

        <div class="card p-4">
          <h3 class="mb-3 text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.usageMonitor.topUsers') }}</h3>
          <div class="space-y-3">
            <div
              v-for="(user, index) in usageMonitorData?.top_users ?? []"
              :key="user.user_id"
              class="rounded-lg border border-gray-200 p-3 dark:border-gray-700"
            >
              <div class="flex items-center justify-between gap-3">
                <div class="min-w-0">
                  <div class="truncate text-sm font-medium text-gray-900 dark:text-white">
                    {{ user.email || `User #${user.user_id}` }}
                  </div>
                  <div class="text-xs text-gray-500 dark:text-gray-400">#{{ index + 1 }} / {{ user.user_id }}</div>
                </div>
                <div class="text-sm font-semibold text-gray-900 dark:text-white">${{ formatCost(user.total_actual_cost) }}</div>
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
        </div>
        <div class="h-[360px]">
          <Line v-if="topUserChartData" :data="topUserChartData" :options="topUserChartOptions" />
          <div v-else class="flex h-full items-center justify-center text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.usageMonitor.noData') }}
          </div>
        </div>
      </div>

      <div class="card p-4">
        <div class="mb-4 flex items-center justify-between gap-3">
          <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.dashboard.spendingRankingTitle') }}</h3>
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
                class="border-t border-gray-100 dark:border-gray-700"
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
import type { TrendDataPoint, UserSpendingRankingItem, UsageCostMonitorData } from '@/types'

ChartJS.register(CategoryScale, LinearScale, LineElement, PointElement, Tooltip, Legend, Filler)

const { t } = useI18n()

type Granularity = 'day' | 'week' | 'month'
type TrendMetric = 'requests' | 'tokens' | 'actual_cost'

const loading = ref(false)
const summaryTrend = ref<TrendDataPoint[]>([])
const usageMonitorData = ref<UsageCostMonitorData | null>(null)
const rankingItems = ref<UserSpendingRankingItem[]>([])

const granularity = ref<Granularity>('day')
const trendMetric = ref<TrendMetric>('actual_cost')
const startDate = ref('')
const endDate = ref('')

const userKeyword = ref('')
const userResults = ref<SimpleUser[]>([])
const userLoading = ref(false)
const showUserDropdown = ref(false)
const selectedUser = ref<SimpleUser | null>(null)
const userDropdownRef = ref<HTMLElement | null>(null)
let userSearchTimer: number | undefined

const granularityOptions = computed(() => ([
  { value: 'day', label: t('admin.usageMonitor.day') },
  { value: 'week', label: t('admin.usageMonitor.week') },
  { value: 'month', label: t('admin.usageMonitor.month') }
]))

const trendMetricOptions = computed(() => ([
  { value: 'actual_cost', label: t('admin.usageMonitor.actualCost') },
  { value: 'tokens', label: t('admin.dashboard.tokens') },
  { value: 'requests', label: t('admin.dashboard.requests') }
]))

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
const formatHourLabel = (bucket: string, offsetHours = 0) => {
  const parsed = parseBucketDate(bucket)
  if (!parsed) return bucket.includes(' ') ? bucket.slice(11, 16) : bucket
  const displayTime = offsetHours ? addHours(parsed, offsetHours) : parsed
  return `${String(displayTime.getHours()).padStart(2, '0')}:00`
}
const formatHourTooltipLabel = (bucket: string, offsetHours = 0) => {
  const parsed = parseBucketDate(bucket)
  if (!parsed) return bucket
  const displayTime = offsetHours ? addHours(parsed, offsetHours) : parsed
  return `${formatDate(displayTime)} ${String(displayTime.getHours()).padStart(2, '0')}:00`
}
const currentDayWindow = () => {
  const end = nextHour()
  return {
    start: new Date(end.getTime() - DAY_WINDOW_HOURS * HOUR_MS),
    end
  }
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

const summaryTrendChartData = computed(() => {
  if (!summaryTrend.value.length) return null
  const dayWindow = granularity.value === 'day' ? currentDayWindow() : null
  const trendItems = dayWindow
    ? summaryTrend.value.filter(item => {
      const bucketTime = parseBucketDate(item.date)
      return bucketTime && bucketTime >= dayWindow.start && bucketTime < dayWindow.end
    })
    : summaryTrend.value
  if (!trendItems.length) return null

  const labels = trendItems.map(item => granularity.value === 'day' ? formatHourLabel(item.date, 1) : item.date)
  const data = trendItems.map(item => {
    if (trendMetric.value === 'requests') return item.requests
    if (trendMetric.value === 'tokens') return item.total_tokens
    return item.actual_cost
  })
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

const pointLookup = computed(() => {
  const map = new Map<string, { actual_cost: number; models: { model: string; actual_cost: number }[] }>()
  for (const item of usageMonitorData.value?.series ?? []) {
    map.set(`${item.user_id}:${item.bucket}`, { actual_cost: item.actual_cost, models: item.models })
  }
  return map
})

const topUserChartData = computed(() => {
  if (!usageMonitorData.value?.series.length) return null
  const buckets = sortBuckets(Array.from(new Set(usageMonitorData.value.series.map(item => item.bucket))))
  const palette = ['#2563eb', '#16a34a', '#d97706', '#dc2626', '#7c3aed']
  return {
    labels: buckets.map(label => granularity.value === 'day' ? formatHourLabel(label, 1) : label),
    datasets: usageMonitorData.value.top_users.map((user, index) => {
      const userMap = new Map(
        usageMonitorData.value!.series.filter(item => item.user_id === user.user_id).map(item => [item.bucket, item.actual_cost])
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

const tooltipBase = {
  backgroundColor: 'rgba(17,24,39,0.96)',
  titleColor: '#ffffff',
  bodyColor: '#e5e7eb',
  borderColor: 'rgba(148,163,184,0.28)',
  borderWidth: 1,
  padding: 14,
  cornerRadius: 16,
  displayColors: true,
  usePointStyle: true,
  bodySpacing: 6,
  titleSpacing: 6
}

const summaryTrendChartOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  interaction: { intersect: false, mode: 'index' as const, axis: 'x' as const },
  plugins: {
    legend: { display: false },
    tooltip: {
      ...tooltipBase,
      callbacks: {
        title: (items: any[]) => items[0]?.label || '',
        label: (ctx: any) => {
          if (trendMetric.value === 'requests') return `${ctx.dataset.label}: ${formatNumber(Number(ctx.raw ?? 0))}`
          if (trendMetric.value === 'tokens') return `${ctx.dataset.label}: ${formatTokens(Number(ctx.raw ?? 0))}`
          return `${ctx.dataset.label}: $${formatCost(Number(ctx.raw ?? 0))}`
        }
      }
    }
  },
  scales: {
    x: {
      ticks: {
        autoSkip: true,
        autoSkipPadding: 24,
        maxRotation: 0,
        minRotation: 0,
        maxTicksLimit: granularity.value === 'day' ? 6 : 10
      }
    },
    y: {
      ticks: {
        callback: (value: string | number) => {
          if (trendMetric.value === 'requests') return String(value)
          if (trendMetric.value === 'tokens') return formatTokens(Number(value))
          return `$${value}`
        }
      }
    }
  }
}))

const topUserChartOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  interaction: { intersect: false, mode: 'index' as const, axis: 'x' as const },
  plugins: {
    legend: { position: 'top' as const, labels: { usePointStyle: true, pointStyle: 'circle' } },
    tooltip: {
      ...tooltipBase,
      callbacks: {
        title: (items: any[]) => {
          const bucket = topUserBucketKeys.value[items[0]?.dataIndex ?? -1]
          if (granularity.value === 'day' && bucket) return `时间 ${formatHourTooltipLabel(bucket, 1)}`
          return `时间 ${items[0]?.label || ''}`
        },
        label: (ctx: any) => {
          const userId = Number(ctx.dataset.userId ?? 0)
          const bucketLabel = topUserBucketKeys.value[ctx.dataIndex] || String(ctx.label || '')
          const point = pointLookup.value.get(`${userId}:${bucketLabel}`)
          const userLabel = String(ctx.dataset.label || '')
          if (!point) return `${userLabel}: $${formatCost(Number(ctx.raw ?? 0))}`
          return `${userLabel}: $${formatCost(point.actual_cost)}`
        },
        afterBody: (items: any[]) => {
          if (!items.length) return []

          const sections = items.flatMap((item: any, index: number) => {
            const userId = Number(item.dataset.userId ?? 0)
            const userLabel = String(item.dataset.label || `User #${userId}`)
            const bucketLabel = topUserBucketKeys.value[item.dataIndex] || String(item.label || '')
            const point = pointLookup.value.get(`${userId}:${bucketLabel}`)

            const header = `用户 ${userLabel}`
            const details = point?.models?.length
              ? point.models.map(model => `- ${model.model}: $${formatCost(model.actual_cost)}`)
              : [`- ${t('common.noData')}`]

            return index === 0 ? [header, ...details] : ['', header, ...details]
          })

          return sections
        }
      }
    }
  },
  scales: {
    x: {
      ticks: {
        autoSkip: true,
        autoSkipPadding: 24,
        maxRotation: 0,
        minRotation: 0,
        maxTicksLimit: granularity.value === 'day' ? 6 : 10
      }
    },
    y: { ticks: { callback: (value: string | number) => `$${value}` } }
  }
}))

const loadData = async () => {
  loading.value = true
  try {
    const [trendRes, rankingRes, monitorRes] = await Promise.all([
      adminAPI.dashboard.getUsageTrend({
        start_date: startDate.value,
        end_date: endDate.value,
        granularity: granularity.value === 'day' ? 'hour' : 'day',
        user_id: selectedUser.value?.id
      }),
      adminAPI.dashboard.getUserSpendingRanking({
        start_date: startDate.value,
        end_date: endDate.value,
        limit: 10
      }),
      adminAPI.dashboard.getUsageCostMonitor({
        granularity: granularity.value,
        start_date: startDate.value,
        end_date: endDate.value,
        user_id: selectedUser.value?.id,
        timezone: userTimezone()
      })
    ])
    summaryTrend.value = trendRes.trend
    rankingItems.value = rankingRes.ranking
    usageMonitorData.value = monitorRes.data
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
  if (selectedUser.value && keyword !== selectedUser.value.email) {
    selectedUser.value = null
    await loadData()
  }
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
  selectedUser.value = user
  userKeyword.value = user.email
  showUserDropdown.value = false
  loadData()
}

const clearUser = () => {
  selectedUser.value = null
  userKeyword.value = ''
  userResults.value = []
  showUserDropdown.value = false
  loadData()
}

const handleGranularityChange = async () => {
  if (granularity.value === 'day') {
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
  initDayRange()
  await loadData()
  document.addEventListener('click', handleClickOutside)
})

onUnmounted(() => {
  document.removeEventListener('click', handleClickOutside)
})
</script>
