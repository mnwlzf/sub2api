<template>
  <BaseDialog
    :show="show && !!account"
    :title="dialogTitle"
    width="extra-wide"
    :close-on-click-outside="false"
    @close="emit('close')"
  >
    <div v-if="account" class="space-y-4">
      <!-- 全局开关状态提示 -->
      <div
        v-if="settings"
        class="rounded-lg border px-4 py-3 text-sm"
        :class="
          settings.enabled
            ? 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-300'
            : 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-300'
        "
      >
        <div class="flex flex-wrap items-center gap-x-4 gap-y-1">
          <span class="inline-flex items-center gap-1.5 font-medium">
            <Icon :name="settings.enabled ? 'checkCircle' : 'exclamationTriangle'" size="sm" />
            {{
              settings.enabled
                ? t('admin.codex.ticket.global.enabled')
                : t('admin.codex.ticket.global.disabled')
            }}
          </span>
          <span>
            {{
              settings.allow_without_ticket
                ? t('admin.codex.ticket.global.allowWithoutTicketOn')
                : t('admin.codex.ticket.global.allowWithoutTicketOff')
            }}
          </span>
          <span>
            {{
              settings.pool.mode === 'custom'
                ? t('admin.codex.ticket.global.poolCustom', {
                    count: settings.pool.proxy_ids.length
                  })
                : t('admin.codex.ticket.global.poolAll')
            }}
          </span>
          <span v-if="settings.available_proxies">
            {{
              t('admin.codex.ticket.global.availableProxies', {
                count: settings.available_proxies.length
              })
            }}
          </span>
          <span class="text-xs opacity-80">
            {{
              t('admin.codex.ticket.global.cadence', {
                min: settings.cadence.retry_min_seconds,
                max: settings.cadence.retry_max_seconds,
                refresh: settings.cadence.refresh_seconds
              })
            }}
          </span>
        </div>
      </div>
      <p v-else-if="settingsError" class="text-xs text-amber-600 dark:text-amber-400">
        {{ t('admin.codex.ticket.global.loadFailed') }}
      </p>

      <!-- 账号 / 模型参与开关 -->
      <div class="rounded-lg border border-gray-200 dark:border-dark-700">
        <div class="flex items-center justify-between border-b border-gray-100 px-4 py-3 dark:border-dark-700">
          <div>
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.codex.ticket.participation.title') }}
            </h3>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.codex.ticket.participation.description') }}
            </p>
          </div>
          <div class="flex items-center gap-3">
            <span class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.codex.ticket.participation.accountEnabled') }}
            </span>
            <Toggle v-model="participationEnabled" />
            <button
              class="btn btn-primary btn-sm"
              :disabled="savingParticipation || statuses.length === 0"
              @click="saveParticipation"
            >
              {{ savingParticipation ? t('common.saving') : t('common.save') }}
            </button>
          </div>
        </div>
        <div class="grid grid-cols-1 gap-2 px-4 py-3 sm:grid-cols-2 lg:grid-cols-3">
          <label
            v-for="status in statuses"
            :key="status.model"
            class="flex items-center justify-between gap-2 rounded-md border border-gray-100 px-3 py-2 dark:border-dark-700"
          >
            <span class="truncate text-sm text-gray-700 dark:text-gray-300">{{ status.model }}</span>
            <input
              v-model="modelParticipation[status.model]"
              type="checkbox"
              class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
            />
          </label>
        </div>
      </div>

      <!-- Tabs -->
      <div class="flex flex-wrap items-center gap-2 border-b border-gray-200 pb-2 dark:border-dark-700">
        <button
          v-for="item in tabs"
          :key="item.key"
          type="button"
          class="rounded-md px-3 py-1.5 text-sm font-medium transition-colors"
          :class="
            activeTab === item.key
              ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/40 dark:text-primary-300'
              : 'text-gray-600 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700'
          "
          @click="activeTab = item.key"
        >
          {{ t(item.labelKey) }}
        </button>
        <button class="btn btn-secondary btn-sm ml-auto" :disabled="loading" @click="reload">
          <Icon name="refresh" size="sm" class="mr-1" />
          {{ t('common.refresh') }}
        </button>
      </div>

      <div v-if="loadError" class="rounded-md bg-red-50 px-3 py-2 text-sm text-red-600 dark:bg-red-900/20 dark:text-red-300">
        {{ loadError }}
      </div>

      <!-- 当前票据 -->
      <div v-if="activeTab === 'current'" class="space-y-4">
        <div class="flex flex-wrap items-end gap-3">
          <div class="w-64">
            <label class="input-label">{{ t('admin.codex.ticket.current.model') }}</label>
            <Select v-model="selectedModel" :options="modelOptions" />
          </div>
          <button
            class="btn btn-primary"
            :disabled="!canHarvest || harvesting"
            :title="harvestBlockedReasonText"
            @click="harvest"
          >
            <Icon name="beaker" size="sm" class="mr-1" />
            {{ harvesting ? t('admin.codex.ticket.current.harvesting') : t('admin.codex.ticket.current.harvest') }}
          </button>
          <span v-if="manualBlockedReason" class="text-xs text-amber-600 dark:text-amber-400">
            {{ harvestBlockedReasonText }}
          </span>
        </div>

        <div v-if="harvestError" class="rounded-md bg-red-50 px-3 py-2 text-sm text-red-600 dark:bg-red-900/20 dark:text-red-300">
          {{ harvestError }}
        </div>
        <div
          v-else-if="harvestResult"
          class="rounded-md bg-emerald-50 px-3 py-2 text-sm text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300"
        >
          {{
            t('admin.codex.ticket.current.harvestResult', {
              outcome: harvestResult.outcome,
              model: harvestResult.model,
              duration: harvestResult.duration_ms
            })
          }}
        </div>

        <div v-if="!selectedStatus" class="rounded-lg border border-gray-200 p-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-gray-400">
          {{ t('admin.codex.ticket.current.noTicket') }}
        </div>
        <div v-else class="grid grid-cols-1 gap-3 lg:grid-cols-2">
          <div class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
            <div class="flex items-center justify-between">
              <span class="text-sm font-medium text-gray-900 dark:text-white">
                {{ t('admin.codex.ticket.current.ticketState') }}
              </span>
              <span :class="['rounded px-2 py-0.5 text-xs font-medium', stateBadgeClass(selectedState)]">
                {{ t(ticketStateI18nKey(selectedState)) }}
              </span>
            </div>
            <dl class="mt-3 space-y-2 text-sm">
              <div class="flex justify-between gap-4">
                <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.codex.ticket.current.length') }}</dt>
                <dd class="font-mono text-gray-900 dark:text-gray-100">
                  {{ selectedStatus.length }} / {{ selectedStatus.expected_ticket_length }}
                  <span
                    v-if="!isTicketLengthExpected(selectedStatus)"
                    class="ml-1 text-xs text-amber-600 dark:text-amber-400"
                  >
                    {{ t('admin.codex.ticket.current.lengthMismatch') }}
                  </span>
                </dd>
              </div>
              <div class="flex justify-between gap-4">
                <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.codex.ticket.current.capturedAt') }}</dt>
                <dd class="text-gray-900 dark:text-gray-100">{{ formatDateTime(selectedStatus.captured_at) }}</dd>
              </div>
              <div class="flex justify-between gap-4">
                <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.codex.ticket.current.expiresAt') }}</dt>
                <dd class="text-gray-900 dark:text-gray-100">{{ formatDateTime(selectedStatus.expires_at) }}</dd>
              </div>
              <div class="flex justify-between gap-4">
                <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.codex.ticket.current.remaining') }}</dt>
                <dd class="text-gray-900 dark:text-gray-100">
                  {{ formatTicketRemainingSeconds(selectedStatus.remaining_seconds) }}
                </dd>
              </div>
              <div class="flex justify-between gap-4">
                <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.codex.ticket.current.reason') }}</dt>
                <dd class="text-gray-900 dark:text-gray-100">{{ reasonText(selectedStatus.reason) }}</dd>
              </div>
              <div class="flex justify-between gap-4">
                <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.codex.ticket.current.quotaResetAt') }}</dt>
                <dd class="text-gray-900 dark:text-gray-100">{{ formatDateTime(selectedStatus.quota_reset_at) }}</dd>
              </div>
            </dl>
          </div>
          <div class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
            <span class="text-sm font-medium text-gray-900 dark:text-white">
              {{ t('admin.codex.ticket.current.components') }}
            </span>
            <div class="mt-3 space-y-3 text-sm">
              <div class="flex items-center justify-between">
                <span class="text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.current.turnState') }}
                </span>
                <span :class="booleanClass(selectedStatus.turn_state_present)">
                  {{ selectedStatus.turn_state_present ? t('common.yes') : t('common.no') }}
                </span>
              </div>
              <div class="flex items-center justify-between">
                <span class="text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.current.cookie') }}
                </span>
                <span :class="booleanClass(selectedStatus.cookie_present)">
                  {{ selectedStatus.cookie_present ? t('common.yes') : t('common.no') }}
                </span>
              </div>
              <div class="flex items-center justify-between">
                <span class="text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.current.harvestEnabled') }}
                </span>
                <span :class="booleanClass(selectedStatus.harvest_enabled)">
                  {{ selectedStatus.harvest_enabled ? t('common.enabled') : t('common.disabled') }}
                </span>
              </div>
              <div class="flex items-center justify-between">
                <span class="text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.current.harvestPaused') }}
                </span>
                <span :class="booleanClass(!selectedStatus.harvest_paused)">
                  {{ selectedStatus.harvest_paused ? t('common.yes') : t('common.no') }}
                </span>
              </div>
              <div class="flex items-center justify-between">
                <span class="text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.current.blocked') }}
                </span>
                <span :class="booleanClass(!selectedStatus.blocked)">
                  {{ selectedStatus.blocked ? t('common.yes') : t('common.no') }}
                </span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- 打票流水 -->
      <div v-else-if="activeTab === 'history'" class="space-y-3">
        <div class="flex items-center gap-3">
          <div class="w-44">
            <Select v-model="historyFilter" :options="historyFilterOptions" @change="loadHistory(1)" />
          </div>
          <span class="text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.codex.ticket.history.total', { total: historyTotal }) }}
          </span>
        </div>
        <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-700">
          <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-700">
            <thead class="bg-gray-50 dark:bg-dark-800">
              <tr>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.time') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.model') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.outcome') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.trigger') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.httpStatus') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.ticketLength') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.duration') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.proxy') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.reason') }}
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-if="historyLoading">
                <td colspan="9" class="px-3 py-6 text-center text-gray-500 dark:text-gray-400">
                  {{ t('common.loading') }}
                </td>
              </tr>
              <tr v-else-if="historyItems.length === 0">
                <td colspan="9" class="px-3 py-6 text-center text-gray-500 dark:text-gray-400">
                  {{ t('common.noData') }}
                </td>
              </tr>
              <tr v-for="item in historyItems" v-else :key="item.id">
                <td class="whitespace-nowrap px-3 py-2 text-gray-700 dark:text-gray-300">
                  {{ formatDateTime(item.occurred_at) }}
                </td>
                <td class="px-3 py-2 font-mono text-xs text-gray-700 dark:text-gray-300">{{ item.model }}</td>
                <td class="px-3 py-2">
                  <span :class="['rounded px-1.5 py-0.5 text-xs font-medium', outcomeBadgeClass(item.outcome)]">
                    {{ item.outcome }}
                  </span>
                </td>
                <td class="px-3 py-2 text-xs text-gray-600 dark:text-gray-400">{{ item.trigger }}</td>
                <td class="px-3 py-2 text-gray-700 dark:text-gray-300">{{ item.http_status ?? '-' }}</td>
                <td class="px-3 py-2 font-mono text-xs text-gray-700 dark:text-gray-300">
                  {{ item.ticket_length ?? '-' }}
                </td>
                <td class="px-3 py-2 text-gray-700 dark:text-gray-300">{{ item.duration_ms }} ms</td>
                <td class="px-3 py-2 text-xs text-gray-600 dark:text-gray-400">
                  {{ item.proxy_name || item.proxy_id || '-' }}
                </td>
                <td class="px-3 py-2 text-xs text-gray-600 dark:text-gray-400">{{ item.reason_code || '-' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pagination
          v-if="historyTotal > 0"
          :page="historyPage"
          :total="historyTotal"
          :page-size="historyPageSize"
          @update:page="loadHistory"
          @update:pageSize="handleHistoryPageSize"
        />
      </div>

      <!-- 事件时间线 -->
      <div v-else-if="activeTab === 'events'" class="space-y-3">
        <div class="flex items-center gap-3">
          <div class="w-44">
            <Select v-model="eventFilter" :options="eventFilterOptions" @change="loadEvents(1)" />
          </div>
          <span class="text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.codex.ticket.events.total', { total: eventsTotal }) }}
          </span>
        </div>
        <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-700">
          <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-700">
            <thead class="bg-gray-50 dark:bg-dark-800">
              <tr>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.time') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.kind') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.model') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.detail') }}
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-if="eventsLoading">
                <td colspan="4" class="px-3 py-6 text-center text-gray-500 dark:text-gray-400">
                  {{ t('common.loading') }}
                </td>
              </tr>
              <tr v-else-if="eventItems.length === 0">
                <td colspan="4" class="px-3 py-6 text-center text-gray-500 dark:text-gray-400">
                  {{ t('common.noData') }}
                </td>
              </tr>
              <tr v-for="item in eventItems" v-else :key="`${item.kind}-${item.id}`">
                <td class="whitespace-nowrap px-3 py-2 text-gray-700 dark:text-gray-300">
                  {{ formatDateTime(eventTime(item)) }}
                </td>
                <td class="px-3 py-2">
                  <span :class="['rounded px-1.5 py-0.5 text-xs font-medium', eventKindBadgeClass(item)]">
                    {{ item.kind || '-' }}
                  </span>
                </td>
                <td class="px-3 py-2 font-mono text-xs text-gray-700 dark:text-gray-300">{{ item.model || '-' }}</td>
                <td class="px-3 py-2 text-xs text-gray-600 dark:text-gray-400">{{ eventDetail(item) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pagination
          v-if="eventsTotal > 0"
          :page="eventsPage"
          :total="eventsTotal"
          :page-size="eventsPageSize"
          @update:page="loadEvents"
          @update:pageSize="handleEventsPageSize"
        />
      </div>

      <!-- 失效历史 -->
      <div v-else class="space-y-3">
        <div class="flex items-center gap-3">
          <span class="text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.codex.ticket.invalidations.total', { total: invalidationsTotal }) }}
          </span>
        </div>
        <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-700">
          <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-700">
            <thead class="bg-gray-50 dark:bg-dark-800">
              <tr>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.time') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.model') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.reason') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.route') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.httpStatus') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.invalidatedCurrent') }}
                </th>
                <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                  {{ t('admin.codex.ticket.columns.generation') }}
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-if="invalidationsLoading">
                <td colspan="7" class="px-3 py-6 text-center text-gray-500 dark:text-gray-400">
                  {{ t('common.loading') }}
                </td>
              </tr>
              <tr v-else-if="invalidationItems.length === 0">
                <td colspan="7" class="px-3 py-6 text-center text-gray-500 dark:text-gray-400">
                  {{ t('common.noData') }}
                </td>
              </tr>
              <tr
                v-for="item in invalidationItems"
                v-else
                :key="item.id"
                class="cursor-pointer hover:bg-gray-50 dark:hover:bg-dark-700/50"
                @click="openInvalidationDetail(item)"
              >
                <td class="whitespace-nowrap px-3 py-2 text-gray-700 dark:text-gray-300">
                  {{ formatDateTime(item.occurred_at) }}
                </td>
                <td class="px-3 py-2 font-mono text-xs text-gray-700 dark:text-gray-300">{{ item.model }}</td>
                <td class="px-3 py-2 text-xs text-gray-600 dark:text-gray-400">{{ item.reason_code }}</td>
                <td class="px-3 py-2 text-xs text-gray-600 dark:text-gray-400">
                  {{ item.request_kind }} / {{ item.request_route }}
                </td>
                <td class="px-3 py-2 text-gray-700 dark:text-gray-300">
                  {{ item.response_http_status ?? '-' }}
                </td>
                <td class="px-3 py-2">
                  <span :class="booleanClass(!item.invalidated_current)">
                    {{ item.invalidated_current ? t('common.yes') : t('common.no') }}
                  </span>
                </td>
                <td class="px-3 py-2 font-mono text-xs text-gray-600 dark:text-gray-400">
                  {{ shortGeneration(item.ticket_generation_id) }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pagination
          v-if="invalidationsTotal > 0"
          :page="invalidationsPage"
          :total="invalidationsTotal"
          :page-size="invalidationsPageSize"
          @update:page="loadInvalidations"
          @update:pageSize="handleInvalidationsPageSize"
        />
      </div>
    </div>

    <template #footer>
      <button class="btn btn-secondary" @click="emit('close')">{{ t('common.close') }}</button>
    </template>
  </BaseDialog>

  <!-- 失效详情（嵌套弹窗） -->
  <BaseDialog
    :show="detailVisible"
    :title="t('admin.codex.ticket.invalidations.detailTitle')"
    width="wide"
    :z-index="60"
    @close="detailVisible = false"
  >
    <div v-if="detailLoading" class="py-6 text-center text-sm text-gray-500 dark:text-gray-400">
      {{ t('common.loading') }}
    </div>
    <div v-else-if="detailError" class="rounded-md bg-red-50 px-3 py-2 text-sm text-red-600 dark:bg-red-900/20 dark:text-red-300">
      {{ detailError }}
    </div>
    <dl v-else-if="detail" class="space-y-2 text-sm">
      <div class="flex justify-between gap-4">
        <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.codex.ticket.columns.model') }}</dt>
        <dd class="font-mono text-gray-900 dark:text-gray-100">{{ detail.model }}</dd>
      </div>
      <div class="flex justify-between gap-4">
        <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.codex.ticket.columns.reason') }}</dt>
        <dd class="text-gray-900 dark:text-gray-100">{{ detail.reason_code }}</dd>
      </div>
      <div class="flex justify-between gap-4">
        <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.codex.ticket.columns.route') }}</dt>
        <dd class="text-gray-900 dark:text-gray-100">
          {{ detail.request_kind }} / {{ detail.request_route }}
        </dd>
      </div>
      <div class="flex justify-between gap-4">
        <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.codex.ticket.columns.httpStatus') }}</dt>
        <dd class="text-gray-900 dark:text-gray-100">{{ detail.response_http_status ?? '-' }}</dd>
      </div>
      <div class="flex justify-between gap-4">
        <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.codex.ticket.columns.generation') }}</dt>
        <dd class="break-all font-mono text-xs text-gray-900 dark:text-gray-100">
          {{ detail.ticket_generation_id }}
        </dd>
      </div>
      <div class="flex justify-between gap-4">
        <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.codex.ticket.columns.invalidatedCurrent') }}</dt>
        <dd :class="booleanClass(!detail.invalidated_current)">
          {{ detail.invalidated_current ? t('common.yes') : t('common.no') }}
        </dd>
      </div>
      <div class="flex justify-between gap-4">
        <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.codex.ticket.invalidations.presence') }}</dt>
        <dd class="text-gray-900 dark:text-gray-100">
          {{
            t('admin.codex.ticket.invalidations.presenceValue', {
              originalTicket: detail.original_ticket_present ? t('common.yes') : t('common.no'),
              originalCookie: detail.original_cookie_present ? t('common.yes') : t('common.no'),
              returnedCookie: detail.returned_cookie_present ? t('common.yes') : t('common.no'),
              setCookies: detail.returned_set_cookie_count
            })
          }}
        </dd>
      </div>
    </dl>
    <template #footer>
      <button class="btn btn-secondary" @click="detailVisible = false">{{ t('common.close') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import Pagination from '@/components/common/Pagination.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import { adminAPI } from '@/api/admin'
import type { Account } from '@/types'
import type {
  CodexTicketAttempt,
  CodexTicketEvent,
  CodexTicketEventFilter,
  CodexTicketHarvestResult,
  CodexTicketHistoryFilter,
  CodexTicketInvalidationDetail,
  CodexTicketInvalidationSummary,
  CodexTicketSettings,
  CodexTicketStatus,
  CodexTicketStatusesResponse
} from '@/api/admin/codexTicket'
import {
  buildParticipationFromStatuses,
  classifyTicketStatus,
  formatTicketRemainingSeconds,
  isTicketLengthExpected,
  manualHarvestBlockedReason,
  normalizeTicketStatuses,
  readAccountTicketParticipationEnabled,
  readAccountTicketParticipationModels,
  ticketStateI18nKey,
  type CodexTicketDisplayState
} from '@/features/codexTickets/ticketStatus'
import { invalidateTicketStatuses } from '@/features/codexTickets/useCodexTicketStatuses'

const props = defineProps<{
  show: boolean
  account: Account | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'changed', accountId: number): void
}>()

const { t, te } = useI18n()

type TabKey = 'current' | 'history' | 'events' | 'invalidations'

const tabs: Array<{ key: TabKey; labelKey: string }> = [
  { key: 'current', labelKey: 'admin.codex.ticket.tabs.current' },
  { key: 'history', labelKey: 'admin.codex.ticket.tabs.history' },
  { key: 'events', labelKey: 'admin.codex.ticket.tabs.events' },
  { key: 'invalidations', labelKey: 'admin.codex.ticket.tabs.invalidations' }
]
const activeTab = ref<TabKey>('current')

const dialogTitle = computed(() =>
  props.account
    ? t('admin.codex.ticket.title', { name: props.account.name })
    : t('admin.codex.ticket.title', { name: '-' })
)

// ---------- 当前票据 / 全局设置 ----------
const statusesResponse = ref<CodexTicketStatusesResponse | null>(null)
const statuses = computed(() => normalizeTicketStatuses(statusesResponse.value))
const loading = ref(false)
const loadError = ref<string | null>(null)
const settings = ref<CodexTicketSettings | null>(null)
const settingsError = ref<string | null>(null)
const selectedModel = ref('')
const modelOptions = computed<SelectOption[]>(() =>
  statuses.value.map(status => ({ label: status.model, value: status.model }))
)
const selectedStatus = computed<CodexTicketStatus | null>(
  () => statuses.value.find(status => status.model === selectedModel.value) ?? statuses.value[0] ?? null
)
const selectedState = computed<CodexTicketDisplayState>(() =>
  selectedStatus.value ? classifyTicketStatus(selectedStatus.value) : 'unavailable'
)

// ---------- 参与开关 ----------
const participationEnabled = ref(true)
const modelParticipation = reactive<Record<string, boolean>>({})
// 服务端已确认的参与状态：刷新票据状态时用它，避免覆盖刚保存的开关。
const storedParticipationEnabled = ref(true)
const storedParticipationModels = ref<Record<string, boolean>>({})
const savingParticipation = ref(false)

// ---------- 手动打票 ----------
const harvesting = ref(false)
const harvestResult = ref<CodexTicketHarvestResult | null>(null)
const harvestError = ref<string | null>(null)
const manualBlockedReason = computed(() => manualHarvestBlockedReason(statusesResponse.value))
const canHarvest = computed(
  () => !!selectedStatus.value && !manualBlockedReason.value && !harvesting.value
)
const harvestBlockedReasonText = computed(() => {
  const reason = manualBlockedReason.value
  if (!reason) return ''
  const key = `admin.codex.ticket.manualUnavailable.${reason}`
  return te(key) ? t(key) : t('admin.codex.ticket.manualUnavailable.unavailable')
})

// ---------- 流水 ----------
const historyItems = ref<CodexTicketAttempt[]>([])
const historyTotal = ref(0)
const historyPage = ref(1)
const historyPageSize = ref(10)
const historyFilter = ref<CodexTicketHistoryFilter>('all')
const historyLoading = ref(false)
const historyLoaded = ref(false)
const historyFilterOptions = computed<SelectOption[]>(() => [
  { label: t('admin.codex.ticket.history.filterAll'), value: 'all' },
  { label: t('admin.codex.ticket.history.filterSuccess'), value: 'success' }
])

// ---------- 事件 ----------
const eventItems = ref<CodexTicketEvent[]>([])
const eventsTotal = ref(0)
const eventsPage = ref(1)
const eventsPageSize = ref(10)
const eventFilter = ref<CodexTicketEventFilter>('all')
const eventsLoading = ref(false)
const eventsLoaded = ref(false)
const eventFilterOptions = computed<SelectOption[]>(() => [
  { label: t('admin.codex.ticket.events.filterAll'), value: 'all' },
  { label: t('admin.codex.ticket.events.filterAttempts'), value: 'attempts' },
  { label: t('admin.codex.ticket.events.filterSuccess'), value: 'success' },
  { label: t('admin.codex.ticket.events.filterFailure'), value: 'failure' },
  { label: t('admin.codex.ticket.events.filterInvalidation'), value: 'invalidation' }
])

// ---------- 失效历史 ----------
const invalidationItems = ref<CodexTicketInvalidationSummary[]>([])
const invalidationsTotal = ref(0)
const invalidationsPage = ref(1)
const invalidationsPageSize = ref(10)
const invalidationsLoading = ref(false)
const invalidationsLoaded = ref(false)

// ---------- 失效详情 ----------
const detailVisible = ref(false)
const detailLoading = ref(false)
const detailError = ref<string | null>(null)
const detail = ref<CodexTicketInvalidationDetail | null>(null)

const errorMessage = (error: unknown): string => {
  if (typeof error === 'object' && error !== null && 'message' in error) {
    const message = String((error as { message?: unknown }).message ?? '')
    if (message) return message
  }
  return t('admin.codex.ticket.loadFailed')
}

const formatDateTime = (value: string | null | undefined): string => {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString()
}

const reasonText = (reason: string | undefined): string => {
  if (!reason) return '-'
  const key = `admin.codex.ticket.reason.${reason}`
  return te(key) ? t(key) : reason
}

const shortGeneration = (value: string | null | undefined): string => {
  if (!value) return '-'
  return value.length > 12 ? `${value.slice(0, 12)}…` : value
}

const booleanClass = (ok: boolean): string =>
  ok ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'

const stateBadgeClasses: Record<CodexTicketDisplayState, string> = {
  ready: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300',
  expiring: 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300',
  expired: 'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-300',
  missing: 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300',
  blocked: 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300',
  paused: 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-300',
  disabled: 'bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-300',
  not_participating: 'bg-slate-100 text-slate-500 dark:bg-slate-700 dark:text-slate-400',
  unavailable: 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'
}
const stateBadgeClass = (state: CodexTicketDisplayState): string => stateBadgeClasses[state]

const outcomeBadgeClass = (outcome: string): string => {
  switch (outcome) {
    case 'success':
      return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
    case 'miss':
      return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
    default:
      return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  }
}

const eventTime = (event: CodexTicketEvent): string => {
  const value = event.occurred_at
  return typeof value === 'string' ? value : ''
}

const eventDetail = (event: CodexTicketEvent): string => {
  if (event.kind === 'invalidation') {
    const parts: string[] = []
    if (typeof event.reason_code === 'string' && event.reason_code) parts.push(event.reason_code)
    if (typeof event.request_kind === 'string' && event.request_kind) parts.push(event.request_kind)
    if (typeof event.request_route === 'string' && event.request_route) parts.push(event.request_route)
    if (typeof event.response_http_status === 'number') parts.push(`HTTP ${event.response_http_status}`)
    if (event.invalidated_current === true) {
      parts.push(t('admin.codex.ticket.events.invalidatedCurrent'))
    }
    return parts.join(' · ') || '-'
  }
  const parts: string[] = []
  if (typeof event.trigger === 'string' && event.trigger) parts.push(event.trigger)
  if (typeof event.http_status === 'number') parts.push(`HTTP ${event.http_status}`)
  if (typeof event.duration_ms === 'number') parts.push(`${event.duration_ms} ms`)
  if (typeof event.ticket_length === 'number') parts.push(`len ${event.ticket_length}`)
  if (typeof event.reason_code === 'string' && event.reason_code) parts.push(event.reason_code)
  return parts.join(' · ') || '-'
}

const eventKindBadgeClass = (event: CodexTicketEvent): string => {
  if (event.kind === 'invalidation') {
    return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  }
  return outcomeBadgeClass(String(event.kind ?? event.outcome ?? ''))
}

const applyParticipationFromAccount = () => {
  const extra = (props.account?.extra ?? {}) as Record<string, unknown>
  storedParticipationEnabled.value = readAccountTicketParticipationEnabled(extra)
  storedParticipationModels.value = readAccountTicketParticipationModels(extra)
  participationEnabled.value = storedParticipationEnabled.value
  for (const key of Object.keys(modelParticipation)) delete modelParticipation[key]
  const base = buildParticipationFromStatuses(statuses.value, participationEnabled.value)
  for (const [model, enabled] of Object.entries(base.models)) {
    modelParticipation[model] = storedParticipationModels.value[model] ?? enabled
  }
}

// 仅用本地已确认的参与状态刷新开关，避免刷新票据后把刚保存的开关覆盖回旧值。
const applyParticipationFromStatuses = () => {
  for (const key of Object.keys(modelParticipation)) delete modelParticipation[key]
  const base = buildParticipationFromStatuses(statuses.value, participationEnabled.value)
  for (const [model, enabled] of Object.entries(base.models)) {
    modelParticipation[model] = storedParticipationModels.value[model] ?? enabled
  }
}

const loadStatuses = async () => {
  if (!props.account) return
  loading.value = true
  loadError.value = null
  settingsError.value = null
  try {
    const [statusData, settingsData] = await Promise.all([
      adminAPI.codexTicket.getAccountTicketStatuses(props.account.id),
      adminAPI.codexTicket.getTicketSettings().catch(() => null)
    ])
    statusesResponse.value = statusData
    settings.value = settingsData
    if (!settingsData) settingsError.value = 'load_failed'
    if (!selectedModel.value || !normalizeTicketStatuses(statusData).some(s => s.model === selectedModel.value)) {
      selectedModel.value = normalizeTicketStatuses(statusData)[0]?.model ?? ''
    }
    applyParticipationFromStatuses()
  } catch (error) {
    loadError.value = errorMessage(error)
    statusesResponse.value = null
  } finally {
    loading.value = false
  }
}

const reload = async () => {
  harvestResult.value = null
  harvestError.value = null
  await loadStatuses()
  if (historyLoaded.value) await loadHistory(historyPage.value)
  if (eventsLoaded.value) await loadEvents(eventsPage.value)
  if (invalidationsLoaded.value) await loadInvalidations(invalidationsPage.value)
  if (props.account) {
    invalidateTicketStatuses(props.account.id)
    emit('changed', props.account.id)
  }
}

const saveParticipation = async () => {
  if (!props.account) return
  savingParticipation.value = true
  try {
    const saved = await adminAPI.codexTicket.setTicketParticipation(props.account.id, {
      enabled: participationEnabled.value,
      models: { ...modelParticipation }
    })
    storedParticipationEnabled.value = saved.enabled
    storedParticipationModels.value = { ...saved.models }
    await loadStatuses()
    invalidateTicketStatuses(props.account.id)
    emit('changed', props.account.id)
  } catch (error) {
    loadError.value = errorMessage(error)
  } finally {
    savingParticipation.value = false
  }
}

const harvest = async () => {
  if (!props.account || !selectedStatus.value) return
  harvesting.value = true
  harvestError.value = null
  harvestResult.value = null
  try {
    const result = await adminAPI.codexTicket.harvestTicket(
      props.account.id,
      selectedStatus.value.model
    )
    harvestResult.value = result
    await loadStatuses()
    if (historyLoaded.value) await loadHistory(1)
    invalidateTicketStatuses(props.account.id)
    emit('changed', props.account.id)
  } catch (error) {
    harvestError.value = errorMessage(error)
  } finally {
    harvesting.value = false
  }
}

const loadHistory = async (page = 1) => {
  if (!props.account) return
  historyPage.value = page
  historyLoading.value = true
  try {
    const data = await adminAPI.codexTicket.getTicketHistory(props.account.id, {
      page,
      page_size: historyPageSize.value,
      filter: historyFilter.value,
      model: selectedModel.value || undefined
    })
    historyItems.value = Array.isArray(data.items) ? data.items : []
    historyTotal.value = data.total ?? 0
    historyLoaded.value = true
  } catch (error) {
    loadError.value = errorMessage(error)
  } finally {
    historyLoading.value = false
  }
}

const handleHistoryPageSize = (size: number) => {
  historyPageSize.value = size
  void loadHistory(1)
}

const loadEvents = async (page = 1) => {
  if (!props.account) return
  eventsPage.value = page
  eventsLoading.value = true
  try {
    const data = await adminAPI.codexTicket.getTicketEvents(props.account.id, {
      page,
      page_size: eventsPageSize.value,
      filter: eventFilter.value,
      model: selectedModel.value || undefined
    })
    eventItems.value = Array.isArray(data.items) ? data.items : []
    eventsTotal.value = data.total ?? 0
    eventsLoaded.value = true
  } catch (error) {
    loadError.value = errorMessage(error)
  } finally {
    eventsLoading.value = false
  }
}

const handleEventsPageSize = (size: number) => {
  eventsPageSize.value = size
  void loadEvents(1)
}

const loadInvalidations = async (page = 1) => {
  if (!props.account) return
  invalidationsPage.value = page
  invalidationsLoading.value = true
  try {
    const data = await adminAPI.codexTicket.listTicketInvalidations(props.account.id, {
      page,
      page_size: invalidationsPageSize.value,
      model: selectedModel.value || undefined
    })
    invalidationItems.value = Array.isArray(data.items) ? data.items : []
    invalidationsTotal.value = data.total ?? 0
    invalidationsLoaded.value = true
  } catch (error) {
    loadError.value = errorMessage(error)
  } finally {
    invalidationsLoading.value = false
  }
}

const handleInvalidationsPageSize = (size: number) => {
  invalidationsPageSize.value = size
  void loadInvalidations(1)
}

const openInvalidationDetail = async (item: CodexTicketInvalidationSummary) => {
  if (!props.account) return
  detailVisible.value = true
  detailLoading.value = true
  detailError.value = null
  detail.value = null
  try {
    detail.value = await adminAPI.codexTicket.getTicketInvalidation(props.account.id, item.id)
  } catch (error) {
    detailError.value = errorMessage(error)
  } finally {
    detailLoading.value = false
  }
}

const resetState = () => {
  activeTab.value = 'current'
  statusesResponse.value = null
  settings.value = null
  settingsError.value = null
  loadError.value = null
  selectedModel.value = ''
  historyItems.value = []
  historyTotal.value = 0
  historyPage.value = 1
  historyFilter.value = 'all'
  historyLoaded.value = false
  eventItems.value = []
  eventsTotal.value = 0
  eventsPage.value = 1
  eventFilter.value = 'all'
  eventsLoaded.value = false
  invalidationItems.value = []
  invalidationsTotal.value = 0
  invalidationsPage.value = 1
  invalidationsLoaded.value = false
  harvestResult.value = null
  harvestError.value = null
  detailVisible.value = false
  detail.value = null
}

watch(
  () => props.show,
  show => {
    if (!show) return
    resetState()
    applyParticipationFromAccount()
    void loadStatuses()
  },
  { immediate: true }
)

watch(
  () => props.account?.id,
  () => {
    if (!props.show) return
    resetState()
    applyParticipationFromAccount()
    void loadStatuses()
  }
)

watch(activeTab, tab => {
  if (tab === 'history' && !historyLoaded.value) void loadHistory(1)
  if (tab === 'events' && !eventsLoaded.value) void loadEvents(1)
  if (tab === 'invalidations' && !invalidationsLoaded.value) void loadInvalidations(1)
})
</script>
