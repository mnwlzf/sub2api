<template>
  <AppLayout>
    <div class="mx-auto max-w-[1500px] pb-10">
      <header class="mb-5 flex flex-wrap items-end justify-between gap-4">
        <div class="min-w-0">
          <h1 class="text-[22px] font-semibold tracking-tight text-gray-950 dark:text-white">
            {{ t('admin.promptTemplates.list.title') }}
          </h1>
          <p class="mt-1.5 max-w-2xl text-sm leading-relaxed text-gray-500 dark:text-dark-300">
            {{ t('admin.promptTemplates.list.description') }}
          </p>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <label class="flex cursor-pointer items-center gap-2 text-xs text-gray-600 dark:text-dark-300">
            <Toggle :model-value="includeArchived" @update:model-value="handleIncludeArchivedChange" />
            <span class="select-none">{{ t('admin.promptTemplates.list.includeArchived') }}</span>
          </label>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="load">
            {{ t('admin.promptTemplates.list.refresh') }}
          </button>
          <button type="button" class="btn btn-primary btn-sm" data-test="create-template" @click="showCreate = true">
            <Icon name="plus" size="sm" class="mr-1.5" />
            {{ t('admin.promptTemplates.list.create') }}
          </button>
        </div>
      </header>

      <div v-if="loadError" role="alert" class="card p-6">
        <p class="text-sm text-red-700 dark:text-red-300">{{ loadError }}</p>
        <button type="button" class="btn btn-secondary btn-sm mt-3" @click="load">
          {{ t('admin.promptTemplates.reload') }}
        </button>
      </div>

      <div v-else class="card">
        <div class="card-header flex flex-wrap items-center justify-between gap-3">
          <div class="w-full max-w-xs">
            <SearchInput
              v-model="search"
              :placeholder="t('admin.promptTemplates.list.searchPlaceholder')"
            />
          </div>
          <span class="text-xs text-gray-400 dark:text-dark-500">
            {{ t('admin.promptTemplates.list.count', { count: rows.length }) }}
          </span>
        </div>

        <div class="card-body">
          <DataTable
            :columns="columns"
            :data="rows"
            :loading="loading"
            row-key="id"
            :estimate-row-height="64"
          >
            <template #cell-name="{ row }">
              <div class="flex flex-wrap items-center gap-2">
                <RouterLink
                  :to="{ name: 'AdminPromptTemplateDetail', params: { id: row.id } }"
                  class="font-medium text-primary-600 hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
                >
                  {{ row.name }}
                </RouterLink>
                <span v-if="row.archived" class="badge badge-gray">
                  {{ t('admin.promptTemplates.list.archived') }}
                </span>
              </div>
              <p v-if="row.description" class="mt-0.5 max-w-md truncate text-xs text-gray-500 dark:text-dark-400" :title="row.description">
                {{ row.description }}
              </p>
            </template>

            <template #cell-latestVersion="{ row }">
              <span v-if="row.latestVersion" class="font-medium text-gray-900 dark:text-white">
                {{ versionLabel(row.latestVersion) }}
              </span>
              <span v-else-if="row.latestVersionUnavailable" class="text-gray-400 dark:text-dark-500">
                {{ t('admin.promptTemplates.list.latestVersionUnavailable') }}
              </span>
              <span v-else class="text-gray-400 dark:text-dark-500">
                {{ t('admin.promptTemplates.list.latestVersionNone') }}
              </span>
            </template>

            <template #cell-actions="{ row }">
              <RouterLink
                :to="{ name: 'AdminPromptTemplateDetail', params: { id: row.id } }"
                class="btn btn-secondary btn-sm"
              >
                {{ t('admin.promptTemplates.list.openDetail') }}
              </RouterLink>
            </template>

            <template #empty>
              <EmptyState
                :title="t('admin.promptTemplates.list.empty')"
                :description="t('admin.promptTemplates.list.emptyHint')"
                :action-text="t('admin.promptTemplates.list.create')"
                @action="showCreate = true"
              />
            </template>
          </DataTable>
        </div>
      </div>
    </div>

    <CreateTemplateDialog
      :show="showCreate"
      @close="showCreate = false"
      @created="handleCreated"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import SearchInput from '@/components/common/SearchInput.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Column } from '@/components/common/types'
import { useAppStore } from '@/stores/app'
import { extractI18nErrorMessage } from '@/utils/apiError'
import CreateTemplateDialog from './components/CreateTemplateDialog.vue'
import { listTemplates, listVersions } from './api'
import type { PromptTemplate, PromptTemplateVersion } from './types'
import { versionLabel } from './viewModel'
import { formatDateTime } from './format'

interface TemplateRow {
  id: number
  name: string
  description?: string
  archived: boolean
  archivedAt?: string
  revision: number
  updatedAt: string
  updatedAtLabel: string
  latestVersion: PromptTemplateVersion | null
  latestVersionUnavailable: boolean
}

const { t, locale } = useI18n()
const router = useRouter()
const appStore = useAppStore()

const loading = ref(false)
const loadError = ref('')
const includeArchived = ref(false)
const search = ref('')
const templates = ref<PromptTemplate[]>([])
const versionsByTemplate = ref<Record<number, PromptTemplateVersion[] | null>>({})
const showCreate = ref(false)

const columns = computed<Column[]>(() => [
  { key: 'name', label: t('admin.promptTemplates.list.columns.name'), sortable: true },
  { key: 'latestVersion', label: t('admin.promptTemplates.list.columns.latestVersion'), sortable: true },
  { key: 'updatedAtLabel', label: t('admin.promptTemplates.list.columns.updatedAt'), sortable: true },
  { key: 'actions', label: t('admin.promptTemplates.list.columns.actions'), class: 'text-right' },
])

function latestVersionOf(id: number): { version: PromptTemplateVersion | null; unavailable: boolean } {
  const versions = versionsByTemplate.value[id]
  if (versions === undefined || versions === null) return { version: null, unavailable: true }
  if (versions.length === 0) return { version: null, unavailable: false }
  return {
    version: versions.reduce((best, item) => (item.version_no > best.version_no ? item : best), versions[0]),
    unavailable: false,
  }
}

const rows = computed<TemplateRow[]>(() => {
  const keyword = search.value.trim().toLowerCase()
  return templates.value
    .filter((template) => !keyword || template.name.toLowerCase().includes(keyword))
    .map((template) => {
      const latest = latestVersionOf(template.id)
      return {
        id: template.id,
        name: template.name,
        description: template.description,
        archived: Boolean(template.archived_at),
        archivedAt: template.archived_at,
        revision: template.revision,
        updatedAt: template.updated_at,
        updatedAtLabel: formatDateTime(template.updated_at, locale.value),
        latestVersion: latest.version,
        latestVersionUnavailable: latest.unavailable,
      }
    })
})

/**
 * 列表接口不返回版本信息，这里为每个模板补一次版本查询。
 *
 * 这是 best-effort：单个模板查询失败只标记为“暂不可用”，
 * 绝不把失败当成“尚未发布”，否则会误导管理员。
 */
async function loadVersions(items: PromptTemplate[]) {
  const results = await Promise.allSettled(
    items.map(async (template) => ({ id: template.id, versions: await listVersions(template.id) })),
  )
  const next: Record<number, PromptTemplateVersion[] | null> = {}
  for (const result of results) {
    if (result.status === 'fulfilled') {
      next[result.value.id] = result.value.versions
    }
  }
  for (const template of items) {
    if (!(template.id in next)) next[template.id] = null
  }
  versionsByTemplate.value = next
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const items = await listTemplates(includeArchived.value)
    templates.value = items
    await loadVersions(items)
  } catch (error) {
    templates.value = []
    versionsByTemplate.value = {}
    loadError.value = extractI18nErrorMessage(
      error,
      t,
      'admin.promptTemplates.errors',
      t('admin.promptTemplates.list.loadFailed'),
    )
  } finally {
    loading.value = false
  }
}

function handleIncludeArchivedChange(value: boolean) {
  includeArchived.value = value
  void load()
}

function handleCreated(template: PromptTemplate) {
  showCreate.value = false
  appStore.showSuccess(t('admin.promptTemplates.create.success'))
  void router.push({ name: 'AdminPromptTemplateDetail', params: { id: template.id } })
}

onMounted(load)
</script>
