<template>
  <AppLayout>
    <div class="space-y-6 p-4 sm:p-6">
      <!-- Header -->
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-xl font-semibold text-gray-900 dark:text-gray-100">
            {{ t('admin.pelicanProbe.title') }}
          </h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.pelicanProbe.description') }}
          </p>
        </div>
        <div class="flex items-center gap-2">
          <label class="flex items-center gap-2 rounded-lg border border-gray-200 bg-white px-3 py-1.5 text-sm text-gray-600 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-300">
            <Toggle :model-value="globalEnabled" @update:model-value="toggleGlobal" />
            {{ t('admin.pelicanProbe.globalSwitch') }}
          </label>
          <Select
            v-model="groupFilter"
            :options="groupFilterOptions"
            class="w-52"
            @change="handleFilterChange"
          />
          <button @click="reloadAll" class="btn btn-secondary flex items-center gap-1.5 text-sm">
            <Icon name="refresh" size="sm" :stroke-width="2" />
            {{ t('admin.pelicanProbe.refresh') }}
          </button>
          <button
            @click="openCreateDialog"
            class="btn btn-primary flex items-center gap-1.5 text-sm"
          >
            <Icon name="plus" size="sm" :stroke-width="2" />
            {{ t('admin.pelicanProbe.addPlan') }}
          </button>
        </div>
      </div>

      <!-- Stats -->
      <div class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
        <div class="rounded-xl border border-gray-200 bg-white p-3 dark:border-gray-700 dark:bg-gray-800">
          <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.pelicanProbe.statsTotal') }}</div>
          <div class="mt-1 text-xl font-semibold text-gray-900 dark:text-gray-100">{{ stats.total }}</div>
        </div>
        <div class="rounded-xl border border-gray-200 bg-white p-3 dark:border-gray-700 dark:bg-gray-800">
          <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.pelicanProbe.statsPending') }}</div>
          <div class="mt-1 text-xl font-semibold text-amber-600">{{ stats.pending }}</div>
        </div>
        <div class="rounded-xl border border-gray-200 bg-white p-3 dark:border-gray-700 dark:bg-gray-800">
          <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.pelicanProbe.statsPass') }}</div>
          <div class="mt-1 text-xl font-semibold text-emerald-600">{{ stats.pass }}</div>
        </div>
        <div class="rounded-xl border border-gray-200 bg-white p-3 dark:border-gray-700 dark:bg-gray-800">
          <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.pelicanProbe.statsDegraded') }}</div>
          <div class="mt-1 text-xl font-semibold text-rose-600">{{ stats.degraded }}</div>
        </div>
        <div class="rounded-xl border border-gray-200 bg-white p-3 dark:border-gray-700 dark:bg-gray-800">
          <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.pelicanProbe.statsInvalid') }}</div>
          <div class="mt-1 text-xl font-semibold text-gray-500 dark:text-gray-400">{{ stats.invalid }}</div>
        </div>
      </div>

      <!-- Plans -->
      <div class="rounded-xl border border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-800">
        <div class="border-b border-gray-200 px-4 py-3 dark:border-gray-700">
          <span class="text-sm font-medium text-gray-900 dark:text-gray-100">
            {{ t('admin.pelicanProbe.plans') }}
          </span>
        </div>
        <div v-if="plansLoading" class="p-6 text-center text-sm text-gray-500">…</div>
        <div v-else-if="plans.length === 0" class="p-6 text-center text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.pelicanProbe.noPlans') }}
        </div>
        <div v-else class="overflow-x-auto">
          <table class="w-full min-w-[760px] text-left text-sm">
            <thead>
              <tr class="border-b border-gray-200 text-xs text-gray-500 dark:border-gray-700 dark:text-gray-400">
                <th class="px-4 py-2.5 font-medium">{{ t('admin.pelicanProbe.planName') }}</th>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.pelicanProbe.group') }}</th>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.pelicanProbe.model') }}</th>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.pelicanProbe.cronExpression') }}</th>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.pelicanProbe.enabled') }}</th>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.pelicanProbe.public') }}</th>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.pelicanProbe.lastRun') }}</th>
                <th class="px-4 py-2.5 font-medium">{{ t('admin.pelicanProbe.nextRun') }}</th>
                <th class="px-4 py-2.5 text-right font-medium">{{ t('admin.pelicanProbe.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="plan in plans"
                :key="plan.id"
                class="border-b border-gray-100 last:border-0 hover:bg-gray-50 dark:border-gray-700/50 dark:hover:bg-gray-700/30"
              >
                <td class="px-4 py-2.5 font-medium text-gray-900 dark:text-gray-100">
                  {{ plan.name || `#${plan.id}` }}
                </td>
                <td class="px-4 py-2.5 text-gray-600 dark:text-gray-300">{{ groupName(plan.group_id) }}</td>
                <td class="px-4 py-2.5 font-mono text-xs text-gray-600 dark:text-gray-300">{{ plan.model_id }}</td>
                <td class="px-4 py-2.5 font-mono text-xs text-gray-600 dark:text-gray-300">{{ plan.cron_expression }}</td>
                <td class="px-4 py-2.5">
                  <Toggle :model-value="plan.enabled" @update:model-value="togglePlan(plan, $event)" />
                </td>
                <td class="px-4 py-2.5">
                  <Toggle :model-value="plan.is_public" @update:model-value="togglePublic(plan, $event)" />
                </td>
                <td class="px-4 py-2.5 text-xs text-gray-500">{{ formatTime(plan.last_run_at) }}</td>
                <td class="px-4 py-2.5 text-xs text-gray-500">{{ formatTime(plan.next_run_at) }}</td>
                <td class="px-4 py-2.5">
                  <div class="flex items-center justify-end gap-1.5">
                    <button
                      @click="runPlan(plan)"
                      :disabled="runningPlanId === plan.id"
                      class="btn btn-secondary px-2.5 py-1 text-xs disabled:opacity-50"
                    >
                      {{ runningPlanId === plan.id ? t('admin.pelicanProbe.running') : t('admin.pelicanProbe.runNow') }}
                    </button>
                    <button @click="openEditDialog(plan)" class="btn btn-secondary px-2.5 py-1 text-xs">
                      {{ t('admin.pelicanProbe.editPlan') }}
                    </button>
                    <button @click="askDelete(plan)" class="btn btn-danger px-2.5 py-1 text-xs">
                      {{ t('admin.pelicanProbe.deletePlan') }}
                    </button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Results wall -->
      <div>
        <div class="mb-3 flex items-center justify-between">
          <span class="text-sm font-medium text-gray-900 dark:text-gray-100">
            {{ t('admin.pelicanProbe.resultsWall') }}
          </span>
          <span
            v-if="runPolling"
            class="inline-flex items-center gap-1.5 rounded-full bg-sky-50 px-2.5 py-0.5 text-xs font-medium text-sky-700 dark:bg-sky-500/10 dark:text-sky-400"
          >
            <Icon name="refresh" size="xs" class="animate-spin" />
            {{ t('admin.pelicanProbe.probing') }}
          </span>
        </div>
        <div v-if="resultsLoading" class="py-8 text-center text-sm text-gray-500">…</div>
        <div v-else-if="results.length === 0" class="rounded-xl border border-dashed border-gray-300 py-10 text-center text-sm text-gray-500 dark:border-gray-600 dark:text-gray-400">
          {{ t('admin.pelicanProbe.noResults') }}
        </div>
        <div v-else class="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
          <div
            v-for="r in results"
            :key="r.id"
            class="overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-800"
          >
            <!-- verdict ribbon + body (candy answers render as text, pelican as iframe) -->
            <ProbeResultBody :result="r" :test-type="planTestType(r.plan_id)" preview-class="h-56" />
            <div class="space-y-2 border-t border-gray-100 p-3 dark:border-gray-700/60">
              <div class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
                <span class="font-medium text-gray-700 dark:text-gray-200">{{ planLabel(r.plan_id) }}</span>
                <span>·</span>
                <span>{{ formatTime(r.created_at) }}</span>
              </div>
              <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
                <span>{{ t('admin.pelicanProbe.latency') }}: {{ (r.latency_ms / 1000).toFixed(1) }}s</span>
                <span>{{ t('admin.pelicanProbe.tokens') }}: {{ r.prompt_tokens + r.completion_tokens }}</span>
                <span v-if="r.reviewed_by">{{ t('admin.pelicanProbe.reviewedBy') }}: {{ r.reviewed_by }}</span>
              </div>
              <div class="flex items-center gap-2 pt-1">
                <!-- A failed probe produced no model output, so pass/degraded
                     have nothing to judge. Offering the buttons anyway let one
                     click record an upstream rate limit as a quality verdict. -->
                <template v-if="!isUnreviewable(r)">
                  <button @click="openReview(r, 'pass')" class="btn btn-primary flex-1 py-1 text-xs">
                    {{ t('admin.pelicanProbe.markPass') }}
                  </button>
                  <button @click="openReview(r, 'degraded')" class="btn btn-danger flex-1 py-1 text-xs">
                    {{ t('admin.pelicanProbe.markDegraded') }}
                  </button>
                </template>
                <span v-else class="flex-1 text-xs text-gray-500 dark:text-gray-400">
                  {{ t('admin.pelicanProbe.reviewNotApplicable') }}
                </span>
                <button @click="viewSource(r)" class="btn btn-secondary px-2.5 py-1 text-xs">
                  {{ t('admin.pelicanProbe.viewSource') }}
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Create/Edit plan dialog -->
    <BaseDialog
      :show="planDialog.show"
      :title="planDialog.editing ? t('admin.pelicanProbe.editPlan') : t('admin.pelicanProbe.addPlan')"
      width="wide"
      @close="planDialog.show = false"
    >
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div>
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.pelicanProbe.planName') }}
          </label>
          <Input v-model="planForm.name" :placeholder="t('admin.pelicanProbe.planNamePlaceholder')" />
        </div>
        <div>
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.pelicanProbe.group') }}
          </label>
          <Select v-model="planForm.group_id" :options="groupOptions" :disabled="planDialog.editing" />
        </div>
        <div>
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.pelicanProbe.model') }}
          </label>
          <Input v-model="planForm.model_id" placeholder="gpt-5 / claude-..." />
        </div>
        <div>
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.pelicanProbe.schedule') }}
          </label>
          <Select v-model="planForm.schedule_preset" :options="schedulePresetOptions" />
          <p class="input-hint mt-1.5">
            <span v-if="!isCustomSchedule" class="font-mono">{{ planForm.schedule_preset }} · </span>{{ selectedScheduleDesc }}
          </p>
          <p class="input-hint mt-0.5">{{ t('admin.pelicanProbe.scheduleHint') }}</p>
        </div>
        <div v-if="isCustomSchedule" class="sm:col-span-2">
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.pelicanProbe.cronExpression') }}
          </label>
          <Input v-model="planForm.cron_custom" placeholder="0 9 * * *" />
        </div>
        <div>
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.pelicanProbe.testType') }}
          </label>
          <Select v-model="planForm.test_type" :options="testTypeOptions" />
        </div>
        <div>
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.pelicanProbe.effort') }}
          </label>
          <Select v-model="planForm.reasoning_effort" :options="effortOptions" />
          <p class="input-hint mt-1.5">{{ t('admin.pelicanProbe.effortHint') }}</p>
        </div>
        <div v-if="isCandy" class="sm:col-span-2">
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.pelicanProbe.judgeModel') }}
          </label>
          <Input
            v-model="planForm.judge_model_id"
            :placeholder="t('admin.pelicanProbe.judgeModelPlaceholder')"
            :hint="t('admin.pelicanProbe.judgeModelHint')"
          />
        </div>
        <div v-if="isCandy" class="sm:col-span-2">
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.pelicanProbe.judgeKey') }}
          </label>
          <Input
            v-model="planForm.judge_api_key"
            type="password"
            :placeholder="planDialog.editing && editingHasJudgeKey ? '••••••••' : 'sk-...'"
            :hint="planDialog.editing ? t('admin.pelicanProbe.judgeKeyKeepHint') : t('admin.pelicanProbe.judgeKeyHint')"
          />
          <p class="input-hint mt-0.5">{{ t('admin.pelicanProbe.judgeKeyGroupHint') }}</p>
        </div>
        <div v-if="isCandy" class="sm:col-span-2">
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.pelicanProbe.judgeBaseUrl') }}
          </label>
          <Input
            v-model="planForm.judge_base_url"
            placeholder="https://your-domain.com"
            :hint="t('admin.pelicanProbe.judgeBaseUrlHint')"
          />
        </div>
        <div class="sm:col-span-2">
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.pelicanProbe.probeKey') }}
          </label>
          <Input
            v-model="planForm.api_key"
            type="password"
            :placeholder="planDialog.editing ? '••••••••' : 'sk-...'"
            :hint="planDialog.editing ? t('admin.pelicanProbe.probeKeyKeepHint') : t('admin.pelicanProbe.probeKeyHint')"
          />
        </div>
        <div class="sm:col-span-2">
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.pelicanProbe.baseUrl') }}
          </label>
          <Input
            v-model="planForm.base_url"
            placeholder="https://your-domain.com"
            :hint="t('admin.pelicanProbe.baseUrlHint')"
          />
        </div>
        <div class="sm:col-span-2">
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.pelicanProbe.prompt') }}
          </label>
          <textarea
            :value="isCandy ? candyPrompt : planForm.prompt"
            :disabled="isCandy"
            :rows="isCandy ? 10 : 2"
            :placeholder="defaultPrompt"
            class="input w-full"
            @input="planForm.prompt = ($event.target as HTMLTextAreaElement).value"
          />
          <p class="input-hint mt-1.5">{{ isCandy ? t('admin.pelicanProbe.candyPromptHint') : t('admin.pelicanProbe.promptHint') }}</p>
        </div>
        <div>
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.pelicanProbe.maxResults') }}
          </label>
          <Input v-model="planForm.max_results" type="number" placeholder="50" />
        </div>
        <div class="flex items-end gap-4 pb-1">
          <label class="flex items-center gap-2 text-xs font-medium text-gray-600 dark:text-gray-400">
            <Toggle v-model="planForm.enabled" />
            {{ t('admin.pelicanProbe.enabled') }}
          </label>
          <label class="flex items-center gap-2 text-xs font-medium text-gray-600 dark:text-gray-400" :title="t('admin.pelicanProbe.publicHint')">
            <Toggle v-model="planForm.is_public" />
            {{ t('admin.pelicanProbe.public') }}
          </label>
        </div>
      </div>
      <template #footer>
        <div class="flex justify-end gap-2">
          <button @click="planDialog.show = false" class="btn btn-secondary text-sm">
            {{ t('admin.pelicanProbe.close') }}
          </button>
          <button @click="savePlan" :disabled="planSaving" class="btn btn-primary text-sm disabled:opacity-50">
            {{ planDialog.editing ? t('admin.pelicanProbe.editPlan') : t('admin.pelicanProbe.addPlan') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Review dialog -->
    <BaseDialog
      :show="reviewDialog.show"
      :title="t('admin.pelicanProbe.reviewTitle')"
      width="wide"
      @close="reviewDialog.show = false"
    >
      <div class="space-y-3">
        <ProbeResultBody
          v-if="reviewDialog.result"
          :result="reviewDialog.result"
          :test-type="planTestType(reviewDialog.result.plan_id)"
          preview-class="h-80"
        />
        <div>
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.pelicanProbe.reviewNote') }}
          </label>
          <textarea
            v-model="reviewDialog.note"
            rows="2"
            :placeholder="t('admin.pelicanProbe.reviewNotePlaceholder')"
            class="input w-full"
          />
        </div>
      </div>
      <template #footer>
        <div class="flex justify-end gap-2">
          <button @click="reviewDialog.show = false" class="btn btn-secondary text-sm">
            {{ t('admin.pelicanProbe.close') }}
          </button>
          <button
            v-if="reviewDialog.verdict === 'pass'"
            @click="submitReview"
            :disabled="reviewSaving"
            class="btn btn-primary text-sm disabled:opacity-50"
          >
            {{ t('admin.pelicanProbe.markPass') }}
          </button>
          <button
            v-else
            @click="submitReview"
            :disabled="reviewSaving"
            class="btn btn-danger text-sm disabled:opacity-50"
          >
            {{ t('admin.pelicanProbe.markDegraded') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Source dialog -->
    <BaseDialog
      :show="sourceDialog.show"
      :title="t('admin.pelicanProbe.viewSource')"
      width="wide"
      @close="sourceDialog.show = false"
    >
      <pre class="max-h-[60vh] overflow-auto rounded-lg bg-gray-900 p-3 text-xs text-gray-100">{{ sourceDialog.html }}</pre>
      <template #footer>
        <div class="flex justify-end">
          <button @click="sourceDialog.show = false" class="btn btn-secondary text-sm">
            {{ t('admin.pelicanProbe.close') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <ConfirmDialog
      :show="deleteDialog.show"
      :title="t('admin.pelicanProbe.deletePlan')"
      :message="t('admin.pelicanProbe.confirmDelete')"
      @confirm="confirmDelete"
      @cancel="deleteDialog.show = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import Input from '@/components/common/Input.vue'
import Toggle from '@/components/common/Toggle.vue'
import ProbeResultBody from '@/components/pelican/ProbeResultBody.vue'
import { Icon } from '@/components/icons'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { formatDateTime } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { PelicanProbePlan, PelicanProbeResult, AdminGroup } from '@/types'

const { t } = useI18n()
const appStore = useAppStore()

const defaultPrompt = '创建一个HTML，内容是SVG绘制一个鹈鹕骑自行车的2D动画'

// Fixed candy-test prompt (mirrors backend DefaultCandyPrompt verbatim).
// v2 adds the touch disambiguation (shapes can be picked by feel) and the
// "ANSWER: <n>" contract the backend parses deterministically.
const candyPrompt = `不使用任何外部工具回答以下问题：

在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求。注意：形状可以靠手感分辨，所以摸取时可以按手感选择每次取出的形状，并根据已经摸出的糖果调整后续选择。）

        苹果味  桃子味  西瓜味
圆形       7      9      8
五角星形   7      6      4

要求：
- 先给出简短推导（不超过 200 字）；
- 最后单独一行只输出 ANSWER: <数字>，这一行之后不要再写任何内容。`

const schedulePresets = [
  { value: '*/15 * * * *', labelKey: 'admin.pelicanProbe.schedule15min', descKey: 'admin.pelicanProbe.schedule15minDesc' },
  { value: '0 * * * *', labelKey: 'admin.pelicanProbe.scheduleHourly', descKey: 'admin.pelicanProbe.scheduleHourlyDesc' },
  { value: '0 */4 * * *', labelKey: 'admin.pelicanProbe.schedule4h', descKey: 'admin.pelicanProbe.schedule4hDesc' },
  { value: '0 9 * * *', labelKey: 'admin.pelicanProbe.scheduleDaily', descKey: 'admin.pelicanProbe.scheduleDailyDesc' },
  { value: '0 9 * * 1', labelKey: 'admin.pelicanProbe.scheduleWeekly', descKey: 'admin.pelicanProbe.scheduleWeeklyDesc' }
] as const
const scheduleCustomValue = 'custom'

const reasoningEffortValues = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max'] as const

// --- filter / data ---
const groupFilter = ref<number | null>(null)
const groups = ref<AdminGroup[]>([])
const plans = ref<PelicanProbePlan[]>([])
const results = ref<PelicanProbeResult[]>([])
const plansLoading = ref(false)
const resultsLoading = ref(false)
const runningPlanId = ref<number | null>(null)

const groupFilterOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('admin.pelicanProbe.allGroups') },
  ...groups.value.map((g) => ({ value: g.id, label: g.name }))
])
const groupOptions = computed<SelectOption[]>(() =>
  groups.value.map((g) => ({ value: g.id, label: g.name }))
)

const groupName = (id: number) => groups.value.find((g) => g.id === id)?.name ?? `#${id}`
const planLabel = (planId: number) => {
  const p = plans.value.find((x) => x.id === planId)
  return p ? `${p.name || `#${p.id}`} · ${p.model_id}` : `#${planId}`
}
// Results carry only plan_id; the candy/pelican rendering branch comes from the
// plan. Unknown plans fall back to the pelican (iframe) preview.
const planTestType = (planId: number) =>
  plans.value.find((x) => x.id === planId)?.test_type || 'pelican'

const stats = computed(() => ({
  total: results.value.length,
  // Failures are counted on their own, never as "pending review": a probe that
  // returned nothing has no verdict for an operator to give. Rows recorded
  // before PelicanVerdictInvalid existed still say verdict=pending, so the
  // status check is what actually keeps them out of the queue.
  pending: results.value.filter((r) => r.status !== 'failed' && r.verdict === 'pending').length,
  pass: results.value.filter((r) => r.verdict === 'pass').length,
  degraded: results.value.filter((r) => r.verdict === 'degraded').length,
  invalid: results.value.filter((r) => r.status === 'failed').length
}))

/**
 * A run is unreviewable when the probe itself failed. Deliberately keyed on
 * `status`, not `verdict`, so the rows written before PelicanVerdictInvalid
 * existed (status=failed + verdict=pending) are treated the same as new ones.
 */
const isUnreviewable = (r: PelicanProbeResult): boolean => r.status === 'failed'

const formatTime = (v: string | null) => (v ? formatDateTime(v) : '—')

// apiClient converts axios timeouts into plain objects shaped like
// { status: 0, code: 'ECONNABORTED', ... }.
const isTimeoutError = (e: unknown): boolean =>
  !!e && typeof e === 'object' && (e as { code?: unknown }).code === 'ECONNABORTED'

const globalEnabled = ref(true)
const loadGlobal = async () => {
  try {
    const status = await adminAPI.pelicanProbe.getGlobalStatus()
    globalEnabled.value = status.enabled
  } catch {
    globalEnabled.value = true
  }
}
const toggleGlobal = async (enabled: boolean) => {
  try {
    const status = await adminAPI.pelicanProbe.setGlobalStatus(enabled)
    globalEnabled.value = status.enabled
    appStore.showSuccess(t('admin.pelicanProbe.updateSuccess'))
  } catch (e: unknown) {
    appStore.showError(extractApiErrorMessage(e, t('common.error')))
  }
}

const loadGroups = async () => {
  try {
    groups.value = await adminAPI.groups.getAll()
  } catch {
    groups.value = []
  }
}
const loadPlans = async () => {
  plansLoading.value = true
  try {
    plans.value = await adminAPI.pelicanProbe.listPlans(groupFilter.value ?? undefined)
  } catch (e: unknown) {
    appStore.showError(extractApiErrorMessage(e, t('common.error')))
  } finally {
    plansLoading.value = false
  }
}
const loadResults = async () => {
  resultsLoading.value = true
  try {
    results.value = await adminAPI.pelicanProbe.listRecent(groupFilter.value ?? undefined, 60)
  } catch (e: unknown) {
    appStore.showError(extractApiErrorMessage(e, t('common.error')))
  } finally {
    resultsLoading.value = false
  }
}
const reloadAll = async () => {
  await Promise.all([loadPlans(), loadResults()])
}
const handleFilterChange = () => {
  void reloadAll()
}

// --- plan dialog ---
const planDialog = reactive({ show: false, editing: false, id: 0 })
const planSaving = ref(false)
// Whether the plan being edited already stores a judge key (drives the
// "leave blank to keep it" placeholder).
const editingHasJudgeKey = ref(false)
const planForm = reactive({
  name: '',
  group_id: null as number | null,
  model_id: '',
  api_key: '',
  base_url: '',
  prompt: '',
  test_type: 'pelican',
  reasoning_effort: '',
  judge_model_id: '',
  judge_api_key: '',
  judge_base_url: '',
  schedule_preset: '0 */4 * * *',
  cron_custom: '',
  enabled: true,
  is_public: false,
  max_results: '50'
})
const isCandy = computed(() => planForm.test_type === 'candy')
const isCustomSchedule = computed(() => planForm.schedule_preset === scheduleCustomValue)
const schedulePresetOptions = computed<SelectOption[]>(() => [
  ...schedulePresets.map((p) => ({ value: p.value, label: t(p.labelKey) })),
  { value: scheduleCustomValue, label: t('admin.pelicanProbe.scheduleCustom') }
])
const selectedScheduleDesc = computed(() => {
  const hit = schedulePresets.find((p) => p.value === planForm.schedule_preset)
  return hit ? t(hit.descKey) : t('admin.pelicanProbe.scheduleCustomDesc')
})
const testTypeOptions = computed<SelectOption[]>(() => [
  { value: 'pelican', label: t('admin.pelicanProbe.testTypePelican') },
  { value: 'candy', label: t('admin.pelicanProbe.testTypeCandy') }
])
const effortOptions = computed<SelectOption[]>(() => [
  { value: '', label: t('admin.pelicanProbe.effortUnspecified') },
  ...reasoningEffortValues.map((v) => ({ value: v, label: v }))
])
const resetPlanForm = () => {
  planForm.name = ''
  planForm.group_id = groupFilter.value
  planForm.model_id = ''
  planForm.api_key = ''
  planForm.base_url = ''
  planForm.prompt = ''
  planForm.test_type = 'pelican'
  planForm.reasoning_effort = ''
  planForm.judge_model_id = ''
  planForm.judge_api_key = ''
  planForm.judge_base_url = ''
  planForm.schedule_preset = '0 */4 * * *'
  planForm.cron_custom = ''
  planForm.enabled = true
  planForm.is_public = false
  planForm.max_results = '50'
  editingHasJudgeKey.value = false
}
const openCreateDialog = () => {
  resetPlanForm()
  planDialog.editing = false
  planDialog.id = 0
  planDialog.show = true
}
const openEditDialog = (plan: PelicanProbePlan) => {
  planForm.name = plan.name
  planForm.group_id = plan.group_id
  planForm.model_id = plan.model_id
  planForm.api_key = ''
  planForm.base_url = plan.base_url
  planForm.prompt = ''
  planForm.test_type = plan.test_type || 'pelican'
  planForm.reasoning_effort = plan.reasoning_effort || ''
  planForm.judge_model_id = plan.judge_model_id || ''
  planForm.judge_api_key = ''
  planForm.judge_base_url = plan.judge_base_url || ''
  editingHasJudgeKey.value = !!plan.has_judge_api_key
  if ((schedulePresets.map((p) => p.value) as readonly string[]).includes(plan.cron_expression)) {
    planForm.schedule_preset = plan.cron_expression
    planForm.cron_custom = ''
  } else {
    planForm.schedule_preset = scheduleCustomValue
    planForm.cron_custom = plan.cron_expression
  }
  planForm.enabled = plan.enabled
  planForm.is_public = plan.is_public
  planForm.max_results = String(plan.max_results)
  planDialog.editing = true
  planDialog.id = plan.id
  planDialog.show = true
}
const savePlan = async () => {
  if (!planForm.group_id || !planForm.model_id.trim()) return
  if (!planDialog.editing && !planForm.api_key.trim()) return
  // A judge model is optional: the backend grades the "ANSWER: <n>" line
  // deterministically and only falls back to the judge when a model ignores the
  // contract.
  const cron = isCustomSchedule.value ? planForm.cron_custom.trim() : planForm.schedule_preset
  planSaving.value = true
  try {
    if (planDialog.editing) {
      const payload: Record<string, unknown> = {
        name: planForm.name.trim(),
        model_id: planForm.model_id.trim(),
        base_url: planForm.base_url.trim(),
        test_type: planForm.test_type,
        reasoning_effort: planForm.reasoning_effort,
        cron_expression: cron,
        enabled: planForm.enabled,
        is_public: planForm.is_public,
        max_results: parseInt(planForm.max_results, 10) || 50
      }
      if (planForm.api_key.trim()) payload.api_key = planForm.api_key.trim()
      if (!isCandy.value && planForm.prompt.trim()) payload.prompt = planForm.prompt.trim()
      if (isCandy.value) {
        payload.judge_model_id = planForm.judge_model_id.trim()
        // Empty judge key = keep the stored one (same contract as api_key).
        if (planForm.judge_api_key.trim()) payload.judge_api_key = planForm.judge_api_key.trim()
        payload.judge_base_url = planForm.judge_base_url.trim()
      }
      await adminAPI.pelicanProbe.updatePlan(planDialog.id, payload)
      appStore.showSuccess(t('admin.pelicanProbe.updateSuccess'))
    } else {
      await adminAPI.pelicanProbe.createPlan({
        name: planForm.name.trim(),
        group_id: planForm.group_id,
        model_id: planForm.model_id.trim(),
        api_key: planForm.api_key.trim(),
        base_url: planForm.base_url.trim() || undefined,
        prompt: isCandy.value ? undefined : (planForm.prompt.trim() || undefined),
        test_type: planForm.test_type,
        reasoning_effort: planForm.reasoning_effort || undefined,
        judge_model_id: isCandy.value ? planForm.judge_model_id.trim() : undefined,
        judge_api_key: isCandy.value ? planForm.judge_api_key.trim() || undefined : undefined,
        judge_base_url: isCandy.value ? planForm.judge_base_url.trim() || undefined : undefined,
        cron_expression: cron || undefined,
        enabled: planForm.enabled,
        is_public: planForm.is_public,
        max_results: parseInt(planForm.max_results, 10) || 50
      })
      appStore.showSuccess(t('admin.pelicanProbe.createSuccess'))
    }
    planDialog.show = false
    await reloadAll()
  } catch (e: unknown) {
    appStore.showError(extractApiErrorMessage(e, t('common.error')))
  } finally {
    planSaving.value = false
  }
}
const togglePlan = async (plan: PelicanProbePlan, enabled: boolean) => {
  try {
    await adminAPI.pelicanProbe.updatePlan(plan.id, { enabled })
    plan.enabled = enabled
  } catch (e: unknown) {
    appStore.showError(extractApiErrorMessage(e, t('common.error')))
  }
}
const togglePublic = async (plan: PelicanProbePlan, isPublic: boolean) => {
  try {
    await adminAPI.pelicanProbe.updatePlan(plan.id, { is_public: isPublic })
    plan.is_public = isPublic
  } catch (e: unknown) {
    appStore.showError(extractApiErrorMessage(e, t('common.error')))
  }
}

// --- immediate run (async contract) ---
//
// The backend now answers HTTP 202 immediately and runs the probe in the
// background (up to 10 minutes), so the response body carries no result. The
// legacy synchronous contract returned the finished result inline. Both are
// supported by treating every 2xx as "started" and polling the results list,
// which is also the only way to observe the async outcome.
const RUN_POLL_INTERVAL_MS = 10_000
const RUN_POLL_MAX_MS = 3 * 60 * 1000

const runPolling = ref(false)
let runPollTimer: ReturnType<typeof setTimeout> | null = null
let runPollDeadline = 0

const stopRunPolling = () => {
  if (runPollTimer !== null) {
    clearTimeout(runPollTimer)
    runPollTimer = null
  }
  runPolling.value = false
  runningPlanId.value = null
}

/**
 * Ids of the results already stored for this plan. Used to detect a *new*
 * result regardless of timestamp precision; `null` means the lookup failed and
 * callers must fall back to comparing `created_at` with the click time.
 */
const snapshotResultIds = async (planId: number): Promise<Set<number> | null> => {
  try {
    const rows = await adminAPI.pelicanProbe.listResults(planId, 10)
    return new Set(rows.map((r) => r.id))
  } catch {
    return null
  }
}

const startRunPolling = (planId: number, clickedAt: number, baseline: Set<number> | null) => {
  stopRunPolling()
  runningPlanId.value = planId
  runPolling.value = true
  runPollDeadline = Date.now() + RUN_POLL_MAX_MS

  const tick = async () => {
    runPollTimer = null
    let finished = false
    try {
      const fresh = await adminAPI.pelicanProbe.listResults(planId, 10)
      finished = fresh.some(
        (r) => (baseline ? !baseline.has(r.id) : false) || +new Date(r.created_at) > clickedAt
      )
      if (finished) {
        await reloadAll()
      } else {
        await loadResults()
      }
    } catch {
      // Transient read failure: keep polling until the deadline.
    }
    if (finished || Date.now() >= runPollDeadline) {
      stopRunPolling()
      return
    }
    runPollTimer = setTimeout(tick, RUN_POLL_INTERVAL_MS)
  }

  runPollTimer = setTimeout(tick, RUN_POLL_INTERVAL_MS)
}

const runPlan = async (plan: PelicanProbePlan) => {
  if (runningPlanId.value !== null) return
  runningPlanId.value = plan.id
  const clickedAt = Date.now()
  const baseline = await snapshotResultIds(plan.id)

  let started = false
  try {
    // Any 2xx means the probe was launched; the body is ignored on purpose
    // because it differs between the legacy and async contracts.
    await adminAPI.pelicanProbe.runNow(plan.id)
    started = true
    appStore.showSuccess(t('admin.pelicanProbe.runStarted'))
  } catch (e: unknown) {
    if (isTimeoutError(e)) {
      // Legacy synchronous contract cut off at the 30s axios timeout: the
      // backend persists the result with a detached context, so keep waiting.
      started = true
      appStore.showError(t('admin.pelicanProbe.runTimeout'))
    } else {
      appStore.showError(extractApiErrorMessage(e, t('common.error')))
    }
  }

  if (!started) {
    stopRunPolling()
    return
  }

  await loadResults()
  startRunPolling(plan.id, clickedAt, baseline)
}

// --- delete ---
const deleteDialog = reactive({ show: false, plan: null as PelicanProbePlan | null })
const askDelete = (plan: PelicanProbePlan) => {
  deleteDialog.plan = plan
  deleteDialog.show = true
}
const confirmDelete = async () => {
  if (!deleteDialog.plan) return
  try {
    await adminAPI.pelicanProbe.deletePlan(deleteDialog.plan.id)
    appStore.showSuccess(t('admin.pelicanProbe.deleteSuccess'))
    deleteDialog.show = false
    await reloadAll()
  } catch (e: unknown) {
    appStore.showError(extractApiErrorMessage(e, t('common.error')))
  }
}

// --- review ---
const reviewDialog = reactive({
  show: false,
  result: null as PelicanProbeResult | null,
  verdict: 'pass' as 'pass' | 'degraded',
  note: ''
})
const reviewSaving = ref(false)
const openReview = (r: PelicanProbeResult, verdict: 'pass' | 'degraded') => {
  reviewDialog.result = r
  reviewDialog.verdict = verdict
  reviewDialog.note = r.note ?? ''
  reviewDialog.show = true
}
const submitReview = async () => {
  if (!reviewDialog.result) return
  reviewSaving.value = true
  try {
    const updated = await adminAPI.pelicanProbe.reviewResult(reviewDialog.result.id, {
      verdict: reviewDialog.verdict,
      note: reviewDialog.note.trim()
    })
    const idx = results.value.findIndex((r) => r.id === updated.id)
    if (idx >= 0) results.value[idx] = updated
    appStore.showSuccess(t('admin.pelicanProbe.reviewSuccess'))
    reviewDialog.show = false
  } catch (e: unknown) {
    appStore.showError(extractApiErrorMessage(e, t('common.error')))
  } finally {
    reviewSaving.value = false
  }
}

// --- source ---
const sourceDialog = reactive({ show: false, html: '' })
const viewSource = (r: PelicanProbeResult) => {
  sourceDialog.html = r.raw_html
  sourceDialog.show = true
}

onMounted(async () => {
  await loadGroups()
  await loadGlobal()
  await reloadAll()
})

onBeforeUnmount(() => {
  stopRunPolling()
})
</script>
