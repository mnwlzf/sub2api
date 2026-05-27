<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="card p-4">
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

          <div class="ml-auto text-sm text-gray-500 dark:text-gray-400">
            {{ granularity === 'day' ? t('admin.usageMonitor.last24h') : t('admin.usageMonitor.limitedRange') }}
          </div>
        </div>
      </div>

      <div v-if="loading" class="card flex items-center justify-center py-16">
        <LoadingSpinner />
      </div>

      <template v-else>
        <div class="grid grid-cols-1 gap-4 xl:grid-cols-3">
          <div class="card p-4 xl:col-span-2">
            <div class="mb-4">
              <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.usageMonitor.title') }}</h3>
              <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.usageMonitor.hoverHint') }}</p>
            </div>

            <div class="h-[480px]">
              <Line v-if="chartData" :data="chartData" :options="chartOptions" />
              <div v-else class="flex h-full items-center justify-center text-sm text-gray-500 dark:text-gray-400">
                {{ t('admin.usageMonitor.noData') }}
              </div>
            </div>
          </div>

          <div class="card p-4">
            <h3 class="mb-3 text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.usageMonitor.topUsers') }}</h3>
            <div class="space-y-3">
              <div
                v-for="(user, index) in data?.top_users ?? []"
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
                  <div class="text-sm font-semibold text-gray-900 dark:text-white">
                    ${{ formatCost(user.total_actual_cost) }}
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Chart as ChartJS, CategoryScale, LinearScale, LineElement, PointElement, Tooltip, Legend, Filler } from 'chart.js'
import { Line } from 'vue-chartjs'
import AppLayout from '@/components/layout/AppLayout.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import { adminAPI } from '@/api/admin'
import type { SimpleUser } from '@/api/admin/usage'
import type { UsageCostMonitorData } from '@/types'

ChartJS.register(CategoryScale, LinearScale, LineElement, PointElement, Tooltip, Legend, Filler)

const { t } = useI18n()

const loading = ref(false)
const data = ref<UsageCostMonitorData | null>(null)
const granularity = ref<'day' | 'week' | 'month'>('week')
const startDate = ref('')
const endDate = ref('')

const userKeyword = ref('')
const userResults = ref<SimpleUser[]>([])
const userLoading = ref(false)
const showUserDropdown = ref(false)
const selectedUser = ref<SimpleUser | null>(null)
const userDropdownRef = ref<HTMLElement | null>(null)
let userSearchTimer: number | undefined
const userTimezone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'

const granularityOptions = computed(() => ([
  { value: 'day', label: t('admin.usageMonitor.day') },
  { value: 'week', label: t('admin.usageMonitor.week') },
  { value: 'month', label: t('admin.usageMonitor.month') }
]))

const formatDate = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`

const initRange = () => {
  const end = new Date()
  const start = new Date()
  start.setDate(start.getDate() - 30)
  startDate.value = formatDate(start)
  endDate.value = formatDate(end)
}

const formatCost = (value: number) => value.toFixed(2)

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
    selectedUser.value = null
    await loadData()
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
    const end = new Date()
    const start = new Date(end.getTime() - 24 * 60 * 60 * 1000)
    startDate.value = formatDate(start)
    endDate.value = formatDate(end)
  }
  await loadData()
}

const handleRangeChange = (range: { startDate: string; endDate: string }) => {
  startDate.value = range.startDate
  endDate.value = range.endDate
  loadData()
}

const loadData = async () => {
  loading.value = true
  try {
    const res = await adminAPI.dashboard.getUsageCostMonitor({
      granularity: granularity.value,
      start_date: startDate.value,
      end_date: endDate.value,
      user_id: selectedUser.value?.id,
      timezone: userTimezone()
    })
    data.value = res.data
  } finally {
    loading.value = false
  }
}

const handleClickOutside = (event: MouseEvent) => {
  const target = event.target as Node | null
  if (userDropdownRef.value && target && !userDropdownRef.value.contains(target)) {
    showUserDropdown.value = false
  }
}

const bucketLabels = computed(() => {
  const buckets = new Set<string>()
  for (const item of data.value?.series ?? []) buckets.add(item.bucket)
  return Array.from(buckets)
})

const pointLookup = computed(() => {
  const map = new Map<string, { actual_cost: number; models: { model: string; actual_cost: number }[] }>()
  for (const item of data.value?.series ?? []) {
    map.set(`${item.user_id}:${item.bucket}`, { actual_cost: item.actual_cost, models: item.models })
  }
  return map
})

const chartData = computed(() => {
  if (!data.value?.series.length) return null
  const palette = ['#2563eb', '#16a34a', '#d97706', '#dc2626', '#7c3aed']
  const users = data.value.top_users
  const labels = bucketLabels.value
  const datasets = users.map((user, index) => {
    const points = data.value!.series.filter(item => item.user_id === user.user_id)
    const map = new Map(points.map(item => [item.bucket, item.actual_cost]))
    return {
      label: user.email || `User #${user.user_id}`,
      userId: user.user_id,
      data: labels.map(label => map.get(label) ?? 0),
      borderColor: palette[index % palette.length],
      backgroundColor: `${palette[index % palette.length]}22`,
      pointRadius: 2,
      tension: 0.25,
      fill: false
    }
  })
  return { labels, datasets }
})

const chartOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  interaction: { intersect: false, mode: 'index' as const },
  plugins: {
    legend: { position: 'top' as const },
    tooltip: {
      callbacks: {
        label: (ctx: any) => {
          const userId = Number(ctx.dataset.userId ?? 0)
          const userLabel = String(ctx.dataset.label || '')
          const bucket = String(ctx.label || '')
          const point = pointLookup.value.get(`${userId}:${bucket}`)
          if (!point) return `${userLabel}: $${formatCost(Number(ctx.raw ?? 0))}`
          const modelText = point.models.map(m => `${m.model}: $${formatCost(m.actual_cost)}`).join(', ')
          return [`${userLabel}: $${formatCost(point.actual_cost)}`, modelText || t('common.noData')]
        }
      }
    }
  },
  scales: {
    x: { ticks: { maxRotation: 0 } },
    y: { ticks: { callback: (value: string | number) => `$${value}` } }
  }
}))

onMounted(async () => {
  initRange()
  await loadData()
  document.addEventListener('click', handleClickOutside)
})

onUnmounted(() => {
  document.removeEventListener('click', handleClickOutside)
})
</script>
