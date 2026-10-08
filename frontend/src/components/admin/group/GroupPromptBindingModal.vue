<template>
  <BaseDialog
    :show="show"
    :title="t('admin.promptTemplates.binding.title')"
    width="extra-wide"
    @close="handleClose"
  >
    <div v-if="group" class="space-y-5">
      <div class="flex flex-wrap items-center gap-3 rounded-lg bg-gray-50 px-4 py-2.5 text-sm dark:bg-dark-700">
        <PlatformIcon :platform="group.platform" size="sm" />
        <span class="font-medium text-gray-900 dark:text-white">{{ group.name }}</span>
        <span class="text-gray-400">#{{ group.id }}</span>
        <span v-if="binding" class="ml-auto text-xs text-gray-500 dark:text-dark-400">
          {{ t('admin.promptTemplates.binding.revision', { revision: binding.revision }) }}
        </span>
      </div>

      <p class="text-sm text-gray-500 dark:text-dark-300">
        {{ t('admin.promptTemplates.binding.description') }}
      </p>

      <p v-if="loadError" role="alert" class="rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
        {{ loadError }}
      </p>

      <div v-if="loading" class="py-8 text-center text-sm text-gray-500 dark:text-dark-400">
        {{ t('admin.promptTemplates.loading') }}
      </div>

      <template v-else-if="binding">
        <p
          v-if="isDisabled"
          role="status"
          class="rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-sm text-gray-700 dark:border-dark-600 dark:bg-dark-700/50 dark:text-dark-200"
        >
          {{ t('admin.promptTemplates.binding.groupDisabledNotice') }}
        </p>
        <p
          v-else-if="boundVersionLabel"
          role="status"
          class="rounded-lg border border-sky-200 bg-sky-50 px-4 py-2.5 text-sm text-sky-900 dark:border-sky-900/70 dark:bg-sky-950/30 dark:text-sky-200"
        >
          {{ t('admin.promptTemplates.binding.fixedVersionNotice', { version: boundVersionLabel }) }}
        </p>

        <p
          v-if="boundTemplateArchived"
          role="status"
          class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-2.5 text-sm text-amber-900 dark:border-amber-900/70 dark:bg-amber-950/30 dark:text-amber-200"
        >
          {{ t('admin.promptTemplates.binding.archivedTemplateNotice') }}
        </p>

        <!-- 模式 -->
        <div>
          <p class="mb-2 text-sm font-medium text-gray-700 dark:text-dark-200">
            {{ t('admin.promptTemplates.binding.mode') }}
          </p>
          <div class="flex flex-wrap gap-3">
            <button
              type="button"
              class="btn"
              :class="mode === 'disabled' ? 'btn-primary' : 'btn-secondary'"
              data-test="binding-mode-disabled"
              @click="mode = 'disabled'"
            >
              {{ t('admin.promptTemplates.binding.disabled') }}
            </button>
            <button
              type="button"
              class="btn"
              :class="mode === 'version' ? 'btn-primary' : 'btn-secondary'"
              data-test="binding-mode-version"
              @click="mode = 'version'"
            >
              {{ t('admin.promptTemplates.binding.version') }}
            </button>
          </div>
        </div>

        <!-- 固定版本 -->
        <div v-if="mode === 'version'">
          <label class="input-label mb-1.5 block text-sm font-medium text-gray-700 dark:text-dark-200" for="binding-version">
            {{ t('admin.promptTemplates.binding.selectVersion') }}
          </label>
          <Select
            id="binding-version"
            v-model="selectedVersionId"
            :options="versionOptions"
            :placeholder="t('admin.promptTemplates.binding.selectVersionPlaceholder')"
            :disabled="versionOptions.length === 0"
            searchable
            data-test="binding-version-select"
          />
          <div v-if="versionOptions.length === 0" class="mt-2 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:border-amber-900/70 dark:bg-amber-950/30 dark:text-amber-200">
            <p class="font-medium">{{ t('admin.promptTemplates.binding.noVersions') }}</p>
            <p class="mt-1">{{ t('admin.promptTemplates.binding.noVersionsHint') }}</p>
            <button type="button" class="btn btn-secondary btn-sm mt-2" @click="goToTemplates">
              {{ t('admin.promptTemplates.binding.goToTemplates') }}
            </button>
          </div>
        </div>

        <div class="flex flex-wrap items-center justify-between gap-3 border-t border-gray-100 pt-4 dark:border-dark-700">
          <span class="text-xs text-gray-500 dark:text-dark-400">
            {{ t('admin.promptTemplates.binding.affectedAccounts') }}:
            {{ accountsLoaded ? t('admin.promptTemplates.binding.affectedAccountsValue', { count: groupAccounts.length }) : t('admin.promptTemplates.binding.affectedAccountsUnknown') }}
          </span>
          <div class="flex items-center gap-3">
            <button
              type="button"
              class="btn btn-secondary"
              :disabled="savingBinding || isDisabled"
              data-test="binding-disable"
              @click="showDisableConfirm = true"
            >
              {{ t('admin.promptTemplates.binding.clear') }}
            </button>
            <button
              type="button"
              class="btn btn-primary"
              :disabled="savingBinding || !bindingDirty || (mode === 'version' && !selectedVersionId)"
              data-test="binding-save"
              @click="saveBinding"
            >
              {{ savingBinding ? t('admin.promptTemplates.binding.saving') : t('admin.promptTemplates.binding.save') }}
            </button>
          </div>
        </div>

        <!-- 账号覆盖 -->
        <section class="border-t border-gray-100 pt-5 dark:border-dark-700">
          <header class="mb-3 flex flex-wrap items-start justify-between gap-3">
            <div>
              <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
                {{ t('admin.promptTemplates.binding.overrides.title') }}
              </h3>
              <p class="mt-1 max-w-3xl text-xs text-gray-500 dark:text-dark-400">
                {{ t('admin.promptTemplates.binding.overrides.description') }}
              </p>
            </div>
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="groupAccounts.length === 0"
              data-test="override-add"
              @click="openOverrideDialog(null)"
            >
              {{ t('admin.promptTemplates.binding.overrides.addOverride') }}
            </button>
          </header>

          <p
            v-if="isDisabled"
            role="status"
            class="mb-3 rounded-lg border border-amber-200 bg-amber-50 px-4 py-2.5 text-xs text-amber-900 dark:border-amber-900/70 dark:bg-amber-950/30 dark:text-amber-200"
          >
            {{ t('admin.promptTemplates.binding.groupDisabledOverridesNotice') }}
          </p>

          <p v-if="overridesError" role="alert" class="mb-3 rounded-lg bg-red-50 px-4 py-2.5 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
            {{ overridesError }}
          </p>
          <p v-if="accountsError" role="alert" class="mb-3 rounded-lg bg-red-50 px-4 py-2.5 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
            {{ accountsError }}
          </p>

          <p v-if="overrides.length === 0" class="rounded-lg border border-dashed border-gray-200 px-4 py-6 text-center text-sm text-gray-500 dark:border-dark-600 dark:text-dark-400">
            {{ t('admin.promptTemplates.binding.overrides.empty') }}
          </p>

          <div v-else class="overflow-hidden rounded-lg border border-gray-200 dark:border-dark-600">
            <div class="max-h-[360px] overflow-auto">
              <table class="w-full min-w-max text-sm">
                <thead class="sticky top-0 z-[1] bg-gray-50 dark:bg-dark-700">
                  <tr>
                    <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.binding.overrides.columns.account') }}</th>
                    <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.binding.overrides.columns.mode') }}</th>
                    <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.binding.overrides.columns.version') }}</th>
                    <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.binding.overrides.currentEffective') }}</th>
                    <th class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.binding.overrides.columns.updatedAt') }}</th>
                    <th class="w-40 px-3 py-2 text-right text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('admin.promptTemplates.binding.overrides.columns.actions') }}</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-gray-100 dark:divide-dark-600">
                  <tr v-for="override in overrides" :key="override.account_id" class="hover:bg-gray-50 dark:hover:bg-dark-700/50">
                    <td class="px-3 py-2 text-gray-900 dark:text-white">
                      {{ accountLabel(override.account_id) }}
                    </td>
                    <td class="px-3 py-2 text-gray-700 dark:text-dark-200">{{ overrideModeLabel(override) }}</td>
                    <td class="px-3 py-2 text-gray-700 dark:text-dark-200">
                      {{ override.version_id ? versionLabelOf(override.version_id) : t('admin.promptTemplates.events.notRecorded') }}
                    </td>
                    <td class="px-3 py-2 text-xs text-gray-500 dark:text-dark-400">{{ effectiveLabel(override) }}</td>
                    <td class="px-3 py-2 text-xs text-gray-500 dark:text-dark-400">{{ formatDateTime(override.updated_at, locale) }}</td>
                    <td class="px-3 py-2 text-right">
                      <div class="flex items-center justify-end gap-2">
                        <button type="button" class="btn btn-secondary btn-sm" @click="openOverrideDialog(override)">
                          {{ t('admin.promptTemplates.binding.overrides.edit') }}
                        </button>
                        <button
                          type="button"
                          class="btn btn-secondary btn-sm text-red-600 dark:text-red-400"
                          @click="requestRemoveOverride(override)"
                        >
                          {{ t('admin.promptTemplates.binding.overrides.clear') }}
                        </button>
                      </div>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </section>
      </template>
    </div>

    <template #footer>
      <div class="flex justify-end">
        <button type="button" class="btn btn-secondary" @click="handleClose">
          {{ t('admin.promptTemplates.binding.overrides.close') }}
        </button>
      </div>
    </template>
  </BaseDialog>

  <GroupPromptOverrideDialog
    :show="showOverrideDialog"
    :group-id="group?.id ?? 0"
    :accounts="groupAccounts"
    :existing="editingOverride"
    :version-options="overrideVersionOptions"
    :group-binding-disabled="isDisabled"
    @close="showOverrideDialog = false"
    @saved="handleOverrideSaved"
    @reload="loadAll"
  />

  <ConfirmDialog
    :show="showDisableConfirm"
    :title="t('admin.promptTemplates.binding.clearConfirmTitle')"
    :message="t('admin.promptTemplates.binding.clearConfirmMessage')"
    :confirm-text="t('admin.promptTemplates.binding.clear')"
    danger
    @confirm="disableBinding"
    @cancel="showDisableConfirm = false"
  />

  <ConfirmDialog
    :show="showRemoveConfirm"
    :title="t('admin.promptTemplates.binding.overrides.removeConfirmTitle')"
    :message="t('admin.promptTemplates.binding.overrides.removeConfirmMessage')"
    :confirm-text="t('admin.promptTemplates.binding.overrides.clear')"
    danger
    @confirm="removeOverride"
    @cancel="pendingRemove = null"
  />

  <ConfirmDialog
    :show="showConflict"
    :title="t('admin.promptTemplates.conflict.title')"
    :message="t('admin.promptTemplates.conflict.message')"
    :confirm-text="t('admin.promptTemplates.conflict.reload')"
    :cancel-text="t('admin.promptTemplates.conflict.dismiss')"
    @confirm="handleConflictReload"
    @cancel="showConflict = false"
  />
</template>

<script setup lang="ts">
import { computed, inject, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { routerKey } from 'vue-router'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Select from '@/components/common/Select.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { adminAPI } from '@/api/admin'
import type { AdminGroup } from '@/types'
import type { SelectOption } from '@/types'
import { useAppStore } from '@/stores/app'
import { extractApiErrorCode, extractI18nErrorMessage } from '@/utils/apiError'
import {
  clearAccountOverride,
  clearGroupBinding,
  getGroupBinding,
  listAccountOverrides,
  listTemplates,
  listVersions,
  setGroupBinding,
} from '@/features/prompt-templates/api'
import type {
  AccountGroupPromptOverride,
  GroupPromptBinding,
  PromptBindingMode,
  PromptTemplate,
  PromptTemplateVersion,
} from '@/features/prompt-templates/types'
import { formatDateTime } from '@/features/prompt-templates/format'
import GroupPromptOverrideDialog from './GroupPromptOverrideDialog.vue'

const props = defineProps<{
  show: boolean
  group: AdminGroup | null
}>()

const emit = defineEmits<{
  close: []
  success: []
}>()

const { t, locale } = useI18n()
const appStore = useAppStore()

interface AccountOption {
  id: number
  name: string
}

// 用 inject(routerKey, null) 而不是 useRouter() / <RouterLink>：后两者在未安装
// router 的既有单测环境里会分别产生 injection 与 resolveComponent 警告噪声。
// 这里取不到 router 时只是不跳转，不影响其它功能。
const router = inject(routerKey, null)
const templatesRoute = { name: 'AdminPromptTemplates' }

const loading = ref(false)
const loadError = ref('')
const binding = ref<GroupPromptBinding | null>(null)
const templates = ref<PromptTemplate[]>([])
const versions = ref<PromptTemplateVersion[]>([])
const overrides = ref<AccountGroupPromptOverride[]>([])
const overridesError = ref('')
const groupAccounts = ref<AccountOption[]>([])
const accountsError = ref('')
const accountsLoaded = ref(false)

const mode = ref<PromptBindingMode>('disabled')
const selectedVersionId = ref<number | null>(null)
const savingBinding = ref(false)

const showDisableConfirm = ref(false)
const showRemoveConfirm = ref(false)
const showConflict = ref(false)
const pendingRemove = ref<AccountGroupPromptOverride | null>(null)

const showOverrideDialog = ref(false)
const editingOverride = ref<AccountGroupPromptOverride | null>(null)

const templatesById = computed(() => {
  const map = new Map<number, PromptTemplate>()
  for (const template of templates.value) map.set(template.id, template)
  return map
})

const versionsById = computed(() => {
  const map = new Map<number, PromptTemplateVersion>()
  for (const version of versions.value) map.set(version.id, version)
  return map
})

const isDisabled = computed(() => mode.value === 'disabled')

const boundVersion = computed(() =>
  binding.value?.version_id ? versionsById.value.get(binding.value.version_id) ?? null : null,
)

const boundVersionLabel = computed(() => versionLabelOf(binding.value?.version_id))

const boundTemplateArchived = computed(() => {
  const version = boundVersion.value
  if (!version) return false
  return Boolean(templatesById.value.get(version.template_id)?.archived_at)
})

const bindingDirty = computed(() => {
  const current = binding.value
  if (!current) return false
  if (current.mode !== mode.value) return true
  if (mode.value === 'disabled') return false
  return (current.version_id ?? null) !== selectedVersionId.value
})

/** 可用于新建绑定的版本：排除已归档模板的版本，但保留当前已绑定的那个。 */
const versionOptions = computed<SelectOption[]>(() => {
  const options: SelectOption[] = []
  for (const version of versions.value) {
    const template = templatesById.value.get(version.template_id)
    const archived = Boolean(template?.archived_at)
    const isBound = binding.value?.version_id === version.id
    if (archived && !isBound) continue
    options.push({
      value: version.id,
      label: `${template?.name ?? `#${version.template_id}`} · v${version.version_no}`,
    })
  }
  return options.sort((a, b) => String(a.label).localeCompare(String(b.label)))
})

/** 账号覆盖可选任意已存在版本（后端只校验版本存在）。 */
const overrideVersionOptions = computed<SelectOption[]>(() =>
  versions.value
    .map((version) => ({
      value: version.id,
      label: `${templatesById.value.get(version.template_id)?.name ?? `#${version.template_id}`} · v${version.version_no}`,
    }))
    .sort((a, b) => String(a.label).localeCompare(String(b.label))),
)

function versionLabelOf(versionId: number | null | undefined): string {
  if (!versionId) return ''
  const version = versionsById.value.get(versionId)
  if (!version) return t('admin.promptTemplates.events.versionValue', { id: versionId })
  return `v${version.version_no}`
}

function accountLabel(accountId: number): string {
  const account = groupAccounts.value.find((item) => item.id === accountId)
  if (account?.name) return account.name
  return t('admin.promptTemplates.events.accountValue', { id: accountId })
}

function overrideModeLabel(override: AccountGroupPromptOverride): string {
  if (override.mode === 'inherit') return t('admin.promptTemplates.binding.overrides.inherit')
  if (override.mode === 'disabled') return t('admin.promptTemplates.binding.overrides.accountDisabled')
  return t('admin.promptTemplates.binding.overrides.accountVersion')
}

/**
 * 当前实际生效说明。
 *
 * 分组绑定是总开关：分组禁用时，账号覆盖一律不生效——这一点必须如实展示。
 */
function effectiveLabel(override: AccountGroupPromptOverride): string {
  if (isDisabled.value) return t('admin.promptTemplates.binding.overrides.effectiveDisabled')
  if (override.mode === 'inherit') {
    if (!binding.value?.version_id) return t('admin.promptTemplates.binding.overrides.effectiveDisabled')
    return t('admin.promptTemplates.binding.overrides.effectiveInherit', { version: boundVersionLabel.value })
  }
  if (override.mode === 'disabled') return t('admin.promptTemplates.binding.overrides.effectiveAccountDisabled')
  return t('admin.promptTemplates.binding.overrides.effectiveAccountVersion', {
    version: versionLabelOf(override.version_id),
  })
}

function isConflict(error: unknown): boolean {
  return extractApiErrorCode(error) === 'PROMPT_REVISION_CONFLICT' || (error as { status?: number })?.status === 409
}

async function loadAll() {
  if (!props.group) return
  const groupId = props.group.id
  loading.value = true
  loadError.value = ''
  overridesError.value = ''
  accountsError.value = ''
  try {
    const [current, templateList] = await Promise.all([
      getGroupBinding(groupId),
      listTemplates(true),
    ])
    binding.value = current
    templates.value = templateList

    // 版本列表用于两个选择器，一次取全量（模板数量在管理端规模有限）。
    const settled = await Promise.allSettled(templateList.map((item) => listVersions(item.id)))
    versions.value = settled.flatMap((result) => (result.status === 'fulfilled' ? result.value : []))

    mode.value = current.mode
    selectedVersionId.value = current.version_id ?? null

    const [overrideResult, accountResult] = await Promise.allSettled([
      listAccountOverrides(groupId),
      adminAPI.accounts.list(1, 200, { group: String(groupId) }),
    ])
    if (overrideResult.status === 'fulfilled') {
      overrides.value = overrideResult.value
    } else {
      overrides.value = []
      overridesError.value = extractI18nErrorMessage(
        overrideResult.reason,
        t,
        'admin.promptTemplates.errors',
        t('admin.promptTemplates.binding.overrides.loadFailed'),
      )
    }
    if (accountResult.status === 'fulfilled') {
      groupAccounts.value = accountResult.value.items.map((account) => ({
        id: account.id,
        name: account.name,
      }))
      accountsLoaded.value = true
    } else {
      groupAccounts.value = []
      accountsLoaded.value = false
      accountsError.value = extractI18nErrorMessage(
        accountResult.reason,
        t,
        'admin.promptTemplates.errors',
        t('admin.promptTemplates.binding.overrides.accountsLoadFailed'),
      )
    }
  } catch (error) {
    binding.value = null
    loadError.value = extractI18nErrorMessage(
      error,
      t,
      'admin.promptTemplates.errors',
      t('admin.promptTemplates.binding.loadFailed'),
    )
  } finally {
    loading.value = false
  }
}

async function saveBinding() {
  if (!props.group || !binding.value) return
  savingBinding.value = true
  try {
    const saved = await setGroupBinding(props.group.id, {
      mode: mode.value,
      version_id: mode.value === 'version' ? selectedVersionId.value : null,
      revision: binding.value.revision,
    })
    binding.value = saved
    mode.value = saved.mode
    selectedVersionId.value = saved.version_id ?? null
    appStore.showSuccess(t('admin.promptTemplates.binding.saveSuccess'))
    emit('success')
  } catch (error) {
    if (isConflict(error)) {
      showConflict.value = true
    } else {
      appStore.showError(
        extractI18nErrorMessage(
          error,
          t,
          'admin.promptTemplates.errors',
          t('admin.promptTemplates.binding.saveFailed'),
        ),
      )
    }
  } finally {
    savingBinding.value = false
  }
}

async function disableBinding() {
  if (!props.group) return
  showDisableConfirm.value = false
  savingBinding.value = true
  try {
    await clearGroupBinding(props.group.id)
    binding.value = { group_id: props.group.id, mode: 'disabled', revision: 0 }
    mode.value = 'disabled'
    selectedVersionId.value = null
    appStore.showSuccess(t('admin.promptTemplates.binding.clearSuccess'))
    emit('success')
  } catch (error) {
    appStore.showError(
      extractI18nErrorMessage(
        error,
        t,
        'admin.promptTemplates.errors',
        t('admin.promptTemplates.binding.clearFailed'),
      ),
    )
  } finally {
    savingBinding.value = false
  }
}

function openOverrideDialog(override: AccountGroupPromptOverride | null) {
  editingOverride.value = override
  showOverrideDialog.value = true
}

function handleOverrideSaved(saved: AccountGroupPromptOverride) {
  const index = overrides.value.findIndex((item) => item.account_id === saved.account_id)
  if (index >= 0) overrides.value[index] = saved
  else overrides.value = [...overrides.value, saved]
  showOverrideDialog.value = false
  appStore.showSuccess(t('admin.promptTemplates.binding.overrides.saveSuccess'))
  emit('success')
}

function requestRemoveOverride(override: AccountGroupPromptOverride) {
  pendingRemove.value = override
  showRemoveConfirm.value = true
}

async function removeOverride() {
  const target = pendingRemove.value
  pendingRemove.value = null
  showRemoveConfirm.value = false
  if (!target || !props.group) return
  try {
    await clearAccountOverride(props.group.id, target.account_id)
    overrides.value = overrides.value.filter((item) => item.account_id !== target.account_id)
    appStore.showSuccess(t('admin.promptTemplates.binding.overrides.clearSuccess'))
    emit('success')
  } catch (error) {
    appStore.showError(
      extractI18nErrorMessage(
        error,
        t,
        'admin.promptTemplates.errors',
        t('admin.promptTemplates.binding.overrides.clearFailed'),
      ),
    )
  }
}

function handleConflictReload() {
  showConflict.value = false
  void loadAll()
}

function handleClose() {
  emit('close')
}

function goToTemplates() {
  emit('close')
  void router?.push(templatesRoute)
}

watch(
  () => props.show,
  (visible) => {
    if (!visible) return
    showDisableConfirm.value = false
    showRemoveConfirm.value = false
    showOverrideDialog.value = false
    editingOverride.value = null
    pendingRemove.value = null
    accountsLoaded.value = false
    void loadAll()
  },
)
</script>
