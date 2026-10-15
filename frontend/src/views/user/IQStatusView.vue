<template>
  <AppLayout>
    <div class="space-y-6 p-4 sm:p-6">
      <!-- Header -->
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-xl font-semibold text-gray-900 dark:text-gray-100">
            {{ t('nav.iqStatus') }}
          </h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.pelicanProbe.description') }}
          </p>
        </div>
        <div class="flex items-center gap-2">
          <Select
            v-model="groupFilter"
            :options="groupFilterOptions"
            class="w-52"
            @change="applyFilter"
          />
          <button @click="load" class="btn btn-secondary flex items-center gap-1.5 text-sm">
            <Icon name="refresh" size="sm" :stroke-width="2" />
            {{ t('admin.pelicanProbe.refresh') }}
          </button>
        </div>
      </div>

      <div v-if="loading" class="py-10 text-center text-sm text-gray-500">…</div>

      <!-- Disabled -->
      <div
        v-else-if="!status?.enabled"
        class="rounded-xl border border-dashed border-gray-300 py-10 text-center text-sm text-gray-500 dark:border-gray-600 dark:text-gray-400"
      >
        {{ t('admin.pelicanProbe.probeDisabled') }}
      </div>

      <!-- Empty -->
      <div
        v-else-if="filteredPlans.length === 0"
        class="rounded-xl border border-dashed border-gray-300 py-10 text-center text-sm text-gray-500 dark:border-gray-600 dark:text-gray-400"
      >
        {{ t('admin.pelicanProbe.noPublicPlans') }}
      </div>

      <div v-else class="space-y-8">
        <section v-for="plan in filteredPlans" :key="plan.id">
          <!-- Plan header with latest verdict -->
          <div class="mb-3 flex flex-wrap items-center gap-2">
            <span class="text-sm font-semibold text-gray-900 dark:text-gray-100">
              {{ plan.name || `#${plan.id}` }}
            </span>
            <span class="text-xs text-gray-500 dark:text-gray-400">
              {{ plan.group_name || `#${plan.group_id}` }} · {{ plan.model_id }}
            </span>
            <span
              v-if="latestByPlan(plan.id)"
              class="rounded-full px-2.5 py-0.5 text-xs font-semibold"
              :class="verdictClass(latestByPlan(plan.id)!.verdict)"
            >
              {{ verdictLabel(latestByPlan(plan.id)!.verdict) }}
            </span>
            <span v-if="latestByPlan(plan.id)" class="text-xs text-gray-400">
              {{ formatTime(latestByPlan(plan.id)!.created_at) }}
            </span>
          </div>

          <!-- Latest result (large card) -->
          <div v-if="latestByPlan(plan.id)" class="overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-800">
            <div class="relative">
              <div
                class="absolute right-3 top-3 z-10 rounded-full px-2.5 py-0.5 text-xs font-semibold shadow"
                :class="verdictClass(latestByPlan(plan.id)!.verdict)"
              >
                {{ verdictLabel(latestByPlan(plan.id)!.verdict) }}
              </div>
              <div v-if="latestByPlan(plan.id)!.status === 'failed'" class="flex h-40 items-center justify-center bg-gray-50 p-4 dark:bg-gray-900">
                <p class="break-all text-xs text-rose-600">{{ latestByPlan(plan.id)!.error_message || t('admin.pelicanProbe.statusFailed') }}</p>
              </div>
              <iframe
                v-else
                sandbox="allow-scripts"
                :srcdoc="latestByPlan(plan.id)!.raw_html"
                class="h-80 w-full bg-white"
                loading="lazy"
                title="pelican probe preview"
              />
            </div>
            <div class="space-y-1.5 border-t border-gray-100 p-3 dark:border-gray-700/60">
              <div class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
                <span>{{ formatTime(latestByPlan(plan.id)!.created_at) }}</span>
                <span>·</span>
                <span>{{ t('admin.pelicanProbe.latency') }}: {{ (latestByPlan(plan.id)!.latency_ms / 1000).toFixed(1) }}s</span>
              </div>
              <p v-if="latestByPlan(plan.id)!.note" class="rounded bg-gray-50 p-1.5 text-xs text-gray-600 dark:bg-gray-900 dark:text-gray-300">
                {{ latestByPlan(plan.id)!.note }}
              </p>
            </div>
          </div>
          <p v-else class="text-xs text-gray-400">{{ t('admin.pelicanProbe.noResults') }}</p>

          <!-- History toggle -->
          <div v-if="historyByPlan(plan.id).length > 0" class="mt-3">
            <button @click="toggleHistory(plan.id)" class="btn btn-secondary px-3 py-1.5 text-xs">
              {{ expandedHistory[plan.id] ? t('admin.pelicanProbe.hideHistory') : t('admin.pelicanProbe.viewHistory', { n: historyByPlan(plan.id).length }) }}
            </button>
          </div>

          <!-- History results -->
          <div v-if="expandedHistory[plan.id]" class="mt-3 grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
            <div
              v-for="r in historyByPlan(plan.id)"
              :key="r.id"
              class="overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-800"
            >
              <div class="relative">
                <div
                  class="absolute right-3 top-3 z-10 rounded-full px-2.5 py-0.5 text-xs font-semibold shadow"
                  :class="verdictClass(r.verdict)"
                >
                  {{ verdictLabel(r.verdict) }}
                </div>
                <div v-if="r.status === 'failed'" class="flex h-48 items-center justify-center bg-gray-50 p-4 dark:bg-gray-900">
                  <p class="break-all text-xs text-rose-600">{{ r.error_message || t('admin.pelicanProbe.statusFailed') }}</p>
                </div>
                <iframe
                  v-else
                  sandbox="allow-scripts"
                  :srcdoc="r.raw_html"
                  class="h-48 w-full bg-white"
                  loading="lazy"
                  title="pelican probe preview"
                />
              </div>
              <div class="space-y-1.5 border-t border-gray-100 p-3 dark:border-gray-700/60">
                <div class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
                  <span>{{ formatTime(r.created_at) }}</span>
                  <span>·</span>
                  <span>{{ t('admin.pelicanProbe.latency') }}: {{ (r.latency_ms / 1000).toFixed(1) }}s</span>
                </div>
                <p v-if="r.note" class="rounded bg-gray-50 p-1.5 text-xs text-gray-600 dark:bg-gray-900 dark:text-gray-300">
                  {{ r.note }}
                </p>
              </div>
            </div>
          </div>
        </section>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import { Icon } from '@/components/icons'
import { getPublicStatus } from '@/api/pelicanProbe'
import { useAppStore } from '@/stores/app'
import { formatDateTime } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { PelicanProbePublicStatus, PelicanProbeResult } from '@/types'

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const status = ref<PelicanProbePublicStatus | null>(null)
const groupFilter = ref<number | null>(null)

const groupFilterOptions = computed<SelectOption[]>(() => {
  const seen = new Map<number, string>()
  for (const p of status.value?.plans ?? []) {
    if (!seen.has(p.group_id)) seen.set(p.group_id, p.group_name || `#${p.group_id}`)
  }
  return [
    { value: null, label: t('admin.pelicanProbe.allGroups') },
    ...[...seen.entries()].map(([id, name]) => ({ value: id, label: name }))
  ]
})

const filteredPlans = computed(() => {
  const plans = status.value?.plans ?? []
  if (groupFilter.value == null) return plans
  return plans.filter((p) => p.group_id === groupFilter.value)
})

const resultsByPlan = (planId: number): PelicanProbeResult[] =>
  (status.value?.results ?? [])
    .filter((r) => r.plan_id === planId)
    .slice()
    .sort((a, b) => +new Date(b.created_at) - +new Date(a.created_at))

const latestByPlan = (planId: number): PelicanProbeResult | undefined =>
  resultsByPlan(planId)[0]

const historyByPlan = (planId: number): PelicanProbeResult[] =>
  resultsByPlan(planId).slice(1)

const expandedHistory = reactive<Record<number, boolean>>({})
const toggleHistory = (planId: number) => {
  expandedHistory[planId] = !expandedHistory[planId]
}

const formatTime = (v: string | null) => (v ? formatDateTime(v) : '—')

const verdictLabel = (v: string) => {
  switch (v) {
    case 'pass':
      return t('admin.pelicanProbe.verdictPass')
    case 'degraded':
      return t('admin.pelicanProbe.verdictDegraded')
    default:
      return t('admin.pelicanProbe.verdictPending')
  }
}
const verdictClass = (v: string) => {
  switch (v) {
    case 'pass':
      return 'bg-emerald-600 text-white'
    case 'degraded':
      return 'bg-rose-700 text-white'
    default:
      return 'bg-amber-500 text-white'
  }
}

const applyFilter = () => {
  // computed handles filtering; kept for Select change event
}

const load = async () => {
  loading.value = true
  try {
    status.value = await getPublicStatus()
  } catch (e: unknown) {
    appStore.showError(extractApiErrorMessage(e, t('common.error')))
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
})
</script>
