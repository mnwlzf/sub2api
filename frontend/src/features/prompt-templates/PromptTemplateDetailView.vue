<template>
  <AppLayout>
    <div class="mx-auto max-w-[1600px] pb-8">
      <RouterLink
        :to="templatesRoute"
        class="mb-4 inline-flex items-center gap-1.5 text-sm text-gray-500 transition-colors hover:text-primary-600 dark:text-dark-400 dark:hover:text-primary-400"
      >
        <Icon name="arrowLeft" size="sm" />
        {{ t('admin.promptTemplates.backToList') }}
      </RouterLink>

      <header class="mb-6 flex flex-wrap items-end justify-between gap-4">
        <div class="min-w-0">
          <h1 class="truncate text-2xl font-semibold tracking-tight text-gray-950 dark:text-white">
            {{ template?.name || t('admin.promptTemplates.title') }}
          </h1>
          <p class="mt-2 max-w-3xl text-sm text-gray-500 dark:text-dark-300">
            {{ t('admin.promptTemplates.description') }}
          </p>
        </div>
        <div v-if="template" class="text-right text-xs text-gray-500 dark:text-dark-400">
          <p>{{ t('admin.promptTemplates.revisionValue', { revision: template.revision }) }}</p>
          <p class="mt-1">{{ t('admin.promptTemplates.savedAt', { time: updatedAtLabel }) }}</p>
        </div>
      </header>

      <div v-if="pageError" role="alert" class="mb-4 rounded-xl border border-red-200 bg-red-50 p-5 dark:border-red-900 dark:bg-red-950/30">
        <p class="text-sm text-red-700 dark:text-red-300">{{ pageError }}</p>
        <button type="button" class="btn btn-secondary btn-sm mt-3" @click="load">
          {{ t('admin.promptTemplates.reload') }}
        </button>
      </div>

      <div v-else-if="pageLoading && !template" class="card p-10 text-center text-sm text-gray-500 dark:text-dark-400">
        {{ t('admin.promptTemplates.loading') }}
      </div>

      <div v-else-if="template" class="space-y-5">
        <TemplateMetaPanel
          :template="template"
          :saving="metaSaving"
          @save="saveMeta"
          @archive="showArchiveConfirm = true"
        />

        <p v-if="draftError" role="alert" class="rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
          {{ draftError }}
        </p>

        <DraftEditor
          v-if="draftForm"
          v-model="draftForm"
          :saving="draftSaving"
          :archived="Boolean(template.archived_at)"
          :dirty="draftDirty"
          @save="saveDraft"
          @reset="resetDraft"
        />

        <ValidationPanel
          :report="validation"
          :loading="validationLoading"
          :error="validationError"
          @refresh="loadValidation"
        />

        <VersionHistoryPanel
          :versions="versions"
          :loading="versionsLoading"
          :error="versionsError"
          :can-publish="canPublish"
          :publishing="publishing"
          @refresh="loadVersions"
          @publish="showPublish = true"
          @preview="openPreview"
          @view-body="openBody"
          @diff="openDiff"
        />

        <TemplateEventPanel
          :events="auditEvents"
          :loading="auditLoading"
          :error="auditError"
          @refresh="loadAuditEvents"
        />
      </div>
    </div>

    <ConfirmDialog
      :show="showConflict"
      :title="t('admin.promptTemplates.conflict.title')"
      :message="t('admin.promptTemplates.conflict.message')"
      :confirm-text="t('admin.promptTemplates.conflict.reload')"
      :cancel-text="t('admin.promptTemplates.conflict.dismiss')"
      @confirm="handleConflictReload"
      @cancel="showConflict = false"
    />

    <ConfirmDialog
      :show="showArchiveConfirm"
      :title="t('admin.promptTemplates.meta.archiveTitle')"
      :message="t('admin.promptTemplates.meta.archiveConfirm')"
      :confirm-text="t('admin.promptTemplates.meta.archive')"
      danger
      @confirm="archiveTemplateNow"
      @cancel="showArchiveConfirm = false"
    />

    <PublishVersionDialog
      :show="showPublish"
      :template-id="templateId"
      :draft-revision="serverDraft?.revision ?? 0"
      :publishing="publishing"
      @close="showPublish = false"
      @publish="publish"
    />

    <VersionPreviewDialog
      :show="showPreview"
      :version="previewTarget"
      @close="closePreview"
    />

    <VersionBodyDialog
      :show="showBody"
      :version="bodyTarget"
      @close="bodyTarget = null"
    />

    <VersionDiffDialog
      :show="showDiff"
      :current="diffTarget"
      :previous="diffPrevious"
      @close="diffTarget = null"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import { extractApiErrorCode, extractI18nErrorMessage } from '@/utils/apiError'
import TemplateMetaPanel from './components/TemplateMetaPanel.vue'
import DraftEditor from './components/DraftEditor.vue'
import ValidationPanel from './components/ValidationPanel.vue'
import VersionHistoryPanel from './components/VersionHistoryPanel.vue'
import TemplateEventPanel from './components/TemplateEventPanel.vue'
import PublishVersionDialog from './components/PublishVersionDialog.vue'
import VersionPreviewDialog from './components/VersionPreviewDialog.vue'
import VersionBodyDialog from './components/VersionBodyDialog.vue'
import VersionDiffDialog from './components/VersionDiffDialog.vue'
import {
  archiveTemplate,
  getDraft,
  getTemplate,
  getValidation,
  listTemplateEvents,
  listVersions,
  publishVersion,
  updateDraft,
  updateTemplate,
} from './api'
import type {
  PromptAdminEvent,
  PromptTemplate,
  PromptTemplateDraft,
  PromptTemplateVersion,
  PromptValidationReport,
  UpdatePromptTemplatePayload,
} from './types'
import { draftToForm, formFingerprint, formToPayload, type PromptDraftForm } from './draftForm'
import { formatDateTime } from './format'

const { t, locale } = useI18n()
const route = useRoute()
const appStore = useAppStore()

const templateId = computed(() => Number(route.params.id))

// 绑定到 setup 变量而不是内联字面量：内联静态 vnode 会被编译器提升到模块作用域，
// 导致未安装 router 的单测环境里出现 "Failed to resolve component: RouterLink" 噪声。
const templatesRoute = { name: 'AdminPromptTemplates' }

const template = ref<PromptTemplate | null>(null)
const serverDraft = ref<PromptTemplateDraft | null>(null)
const draftForm = ref<PromptDraftForm | null>(null)
const validation = ref<PromptValidationReport | null>(null)
const versions = ref<PromptTemplateVersion[]>([])
const auditEvents = ref<PromptAdminEvent[]>([])

const pageLoading = ref(false)
const pageError = ref('')
const draftError = ref('')
const validationError = ref('')
const versionsError = ref('')
const auditError = ref('')
const metaSaving = ref(false)
const draftSaving = ref(false)
const validationLoading = ref(false)
const versionsLoading = ref(false)
const auditLoading = ref(false)
const publishing = ref(false)

const showConflict = ref(false)
const showArchiveConfirm = ref(false)
const showPublish = ref(false)
const showPreview = ref(false)
const showBody = ref(false)
const showDiff = ref(false)
const previewTarget = ref<PromptTemplateVersion | null>(null)
const bodyTarget = ref<PromptTemplateVersion | null>(null)
const diffTarget = ref<PromptTemplateVersion | null>(null)

const updatedAtLabel = computed(() => formatDateTime(template.value?.updated_at, locale.value))

const draftDirty = computed(
  () => formFingerprint(draftForm.value) !== formFingerprint(draftToForm(serverDraft.value)),
)

/** 只有校验通过的已保存草稿才允许发布。 */
const canPublish = computed(() => Boolean(validation.value?.valid) && !template.value?.archived_at)

/** 差异对比的“上一版本”：版本号紧邻且更小的那一个。 */
const diffPrevious = computed(() => {
  const current = diffTarget.value
  if (!current) return null
  const candidates = versions.value.filter((item) => item.version_no < current.version_no)
  if (!candidates.length) return null
  return candidates.reduce((best, item) => (item.version_no > best.version_no ? item : best), candidates[0])
})

function isConflict(error: unknown): boolean {
  return extractApiErrorCode(error) === 'PROMPT_REVISION_CONFLICT' || (error as { status?: number })?.status === 409
}

async function loadVersions() {
  versionsLoading.value = true
  versionsError.value = ''
  try {
    versions.value = await listVersions(templateId.value)
  } catch (error) {
    versionsError.value = extractI18nErrorMessage(
      error,
      t,
      'admin.promptTemplates.errors',
      t('admin.promptTemplates.loadFailed'),
    )
  } finally {
    versionsLoading.value = false
  }
}

/** 只读管理审计事件：不含正文，仅哈希与元数据。 */
async function loadAuditEvents() {
  auditLoading.value = true
  auditError.value = ''
  try {
    auditEvents.value = await listTemplateEvents(templateId.value)
  } catch (error) {
    auditEvents.value = []
    auditError.value = extractI18nErrorMessage(
      error,
      t,
      'admin.promptTemplates.errors',
      t('admin.promptTemplates.audit.loadFailed'),
    )
  } finally {
    auditLoading.value = false
  }
}

async function loadValidation() {
  validationLoading.value = true
  validationError.value = ''
  try {
    validation.value = await getValidation(templateId.value)
  } catch (error) {
    validation.value = null
    validationError.value = extractI18nErrorMessage(
      error,
      t,
      'admin.promptTemplates.errors',
      t('admin.promptTemplates.validation.loadFailed'),
    )
  } finally {
    validationLoading.value = false
  }
}

async function loadDraft() {
  draftError.value = ''
  try {
    const draft = await getDraft(templateId.value)
    serverDraft.value = draft
    draftForm.value = draftToForm(draft)
  } catch (error) {
    serverDraft.value = null
    draftForm.value = null
    draftError.value = extractI18nErrorMessage(
      error,
      t,
      'admin.promptTemplates.errors',
      t('admin.promptTemplates.draft.saveFailed'),
    )
  }
}

async function load() {
  pageLoading.value = true
  pageError.value = ''
  try {
    template.value = await getTemplate(templateId.value)
  } catch (error) {
    template.value = null
    pageError.value = extractI18nErrorMessage(
      error,
      t,
      'admin.promptTemplates.errors',
      t('admin.promptTemplates.loadFailed'),
    )
    pageLoading.value = false
    return
  }
  await Promise.allSettled([loadDraft(), loadValidation(), loadVersions(), loadAuditEvents()])
  pageLoading.value = false
}

async function saveMeta(payload: UpdatePromptTemplatePayload) {
  metaSaving.value = true
  try {
    template.value = await updateTemplate(templateId.value, payload)
    appStore.showSuccess(t('admin.promptTemplates.meta.saveSuccess'))
  } catch (error) {
    if (isConflict(error)) {
      showConflict.value = true
    } else {
      appStore.showError(
        extractI18nErrorMessage(error, t, 'admin.promptTemplates.errors', t('admin.promptTemplates.meta.saveFailed')),
      )
    }
  } finally {
    metaSaving.value = false
  }
}

async function saveDraft() {
  if (!draftForm.value) return
  draftSaving.value = true
  draftError.value = ''
  try {
    // 始终带上服务器已知的修订号：后端据此判定冲突，绝不做静默覆盖。
    const saved = await updateDraft(templateId.value, formToPayload(draftForm.value, serverDraft.value?.revision ?? 0))
    serverDraft.value = saved
    draftForm.value = draftToForm(saved)
    appStore.showSuccess(t('admin.promptTemplates.draft.saveSuccess'))
    await loadValidation()
  } catch (error) {
    if (isConflict(error)) {
      showConflict.value = true
    } else {
      draftError.value = extractI18nErrorMessage(
        error,
        t,
        'admin.promptTemplates.errors',
        t('admin.promptTemplates.draft.saveFailed'),
      )
    }
  } finally {
    draftSaving.value = false
  }
}

function resetDraft() {
  draftForm.value = draftToForm(serverDraft.value)
}

async function archiveTemplateNow() {
  if (!template.value) return
  showArchiveConfirm.value = false
  try {
    await archiveTemplate(templateId.value, template.value.revision)
    appStore.showSuccess(t('admin.promptTemplates.meta.archiveSuccess'))
    await load()
  } catch (error) {
    if (isConflict(error)) {
      showConflict.value = true
    } else {
      appStore.showError(
        extractI18nErrorMessage(error, t, 'admin.promptTemplates.errors', t('admin.promptTemplates.meta.archiveFailed')),
      )
    }
  }
}

async function publish(payload: { changeNote: string; idempotencyKey: string }) {
  publishing.value = true
  try {
    const version = await publishVersion(templateId.value, {
      draft_revision: serverDraft.value?.revision ?? 0,
      change_note: payload.changeNote || undefined,
      idempotency_key: payload.idempotencyKey,
    })
    showPublish.value = false
    appStore.showSuccess(
      t('admin.promptTemplates.versions.publishSuccess', { version: `v${version.version_no}` }),
    )
    await Promise.allSettled([loadVersions(), loadValidation()])
  } catch (error) {
    if (isConflict(error)) {
      showConflict.value = true
      showPublish.value = false
    } else {
      appStore.showError(
        extractI18nErrorMessage(error, t, 'admin.promptTemplates.errors', t('admin.promptTemplates.versions.publishFailed')),
      )
    }
  } finally {
    publishing.value = false
  }
}

function openPreview(version: PromptTemplateVersion) {
  previewTarget.value = version
  showPreview.value = true
}

function closePreview() {
  showPreview.value = false
  previewTarget.value = null
}

function openBody(version: PromptTemplateVersion) {
  bodyTarget.value = version
  showBody.value = true
}

function openDiff(version: PromptTemplateVersion) {
  diffTarget.value = version
  showDiff.value = true
}

function handleConflictReload() {
  showConflict.value = false
  void load()
}

onMounted(load)
</script>
