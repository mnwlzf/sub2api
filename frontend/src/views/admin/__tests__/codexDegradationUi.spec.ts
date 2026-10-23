import { describe, expect, it } from 'vitest'

import {
  buildOpenAIRequestTimezoneOptions,
  applyOpenAIRequestTimezoneExtra,
  normalizeOpenAIRequestTimezone,
  OPENAI_REQUEST_TIMEZONE_EXTRA_KEY
} from '@/components/account/openaiRequestTimezone'
import {
  countDegradationStatuses,
  degradationStatusBadgeClass,
  degradationStatusI18nKey,
  formatDegradationProbability,
  hasDegradedModels,
  normalizeDegradationStatus,
  resolveInitialDiagnosticModels
} from '@/features/codexDegradation/verdict'
import {
  isDefaultProbeTemplate,
  normalizeDegradationSettingsPayload,
  normalizeProbeTemplate,
  validateDegradationSettings
} from '@/features/codexDegradation/settings'

describe('openai request timezone helpers', () => {
  it('builds options with a follow-default entry first', () => {
    const options = buildOpenAIRequestTimezoneOptions(
      'Asia/Singapore',
      ['Asia/Tokyo', 'America/New_York', 'Asia/Tokyo'],
      'Follow default'
    )
    expect(options[0]).toEqual({ label: 'Follow default (Asia/Singapore)', value: '' })
    expect(options.slice(1)).toEqual([
      { label: 'Asia/Tokyo', value: 'Asia/Tokyo' },
      { label: 'America/New_York', value: 'America/New_York' }
    ])
  })

  it('falls back to the backend default when no default is provided', () => {
    const options = buildOpenAIRequestTimezoneOptions('', null, 'Default')
    expect(options[0]).toEqual({ label: 'Default (Asia/Singapore)', value: '' })
    expect(options).toHaveLength(1)
  })

  it('normalizes historical values', () => {
    expect(normalizeOpenAIRequestTimezone('Asia/Tokyo', ['Asia/Tokyo'])).toBe('Asia/Tokyo')
    expect(normalizeOpenAIRequestTimezone('Europe/Paris', ['Asia/Tokyo'])).toBe('')
    expect(normalizeOpenAIRequestTimezone('  ', ['Asia/Tokyo'])).toBe('')
    expect(normalizeOpenAIRequestTimezone(42, ['Asia/Tokyo'])).toBe('')
    // 列表尚未加载时保留原值，避免编辑弹窗初始化阶段误清空。
    expect(normalizeOpenAIRequestTimezone('Europe/Paris', null)).toBe('Europe/Paris')
  })

  it('writes and clears the extra key', () => {
    const extra: Record<string, unknown> = { openai_passthrough: true }
    applyOpenAIRequestTimezoneExtra(extra, 'openai', 'Asia/Tokyo')
    expect(extra[OPENAI_REQUEST_TIMEZONE_EXTRA_KEY]).toBe('Asia/Tokyo')
    applyOpenAIRequestTimezoneExtra(extra, 'openai', '')
    expect(extra).not.toHaveProperty(OPENAI_REQUEST_TIMEZONE_EXTRA_KEY)
    expect(extra.openai_passthrough).toBe(true)
    const anthropicExtra: Record<string, unknown> = { openai_request_timezone: 'Asia/Tokyo' }
    applyOpenAIRequestTimezoneExtra(anthropicExtra, 'anthropic', 'Asia/Tokyo')
    expect(anthropicExtra).not.toHaveProperty(OPENAI_REQUEST_TIMEZONE_EXTRA_KEY)
  })
})

describe('codex degradation verdict helpers', () => {
  it('normalizes unknown statuses to failed', () => {
    expect(normalizeDegradationStatus('degraded')).toBe('degraded')
    expect(normalizeDegradationStatus('bogus')).toBe('failed')
    expect(normalizeDegradationStatus(undefined)).toBe('failed')
    expect(degradationStatusI18nKey('uncertain')).toBe('admin.codex.degradation.status.uncertain')
    expect(degradationStatusBadgeClass('normal')).toContain('emerald')
    expect(degradationStatusBadgeClass('degraded')).toContain('red')
  })

  it('formats probability as percentage', () => {
    expect(formatDegradationProbability(0.876)).toBe('87.6%')
    expect(formatDegradationProbability(0)).toBe('0.0%')
    expect(formatDegradationProbability(undefined)).toBe('-')
  })

  it('counts statuses and detects degraded models', () => {
    const counts = countDegradationStatuses([
      { model: 'a', status: 'normal' },
      { model: 'b', status: 'degraded' },
      { model: 'c', status: 'degraded' },
      { model: 'd', status: 'failed' }
    ])
    expect(counts).toEqual({ normal: 1, degraded: 2, uncertain: 0, failed: 1, total: 4 })
    expect(hasDegradedModels({ items: [{ model: 'b', status: 'degraded' }] })).toBe(true)
    expect(hasDegradedModels({ items: [{ model: 'a', status: 'normal' }] })).toBe(false)
    expect(hasDegradedModels(null)).toBe(false)
  })

  it('resolves initial diagnostic models from settings and available models', () => {
    expect(resolveInitialDiagnosticModels(['gpt-6.1-sol'], ['gpt-6.1-sol', 'gpt-6-astra'])).toEqual([
      'gpt-6.1-sol'
    ])
    expect(resolveInitialDiagnosticModels(['removed-model'], ['gpt-6-astra', 'gpt-6.1-sol'])).toEqual([
      'gpt-6-astra'
    ])
    expect(resolveInitialDiagnosticModels(null, [])).toEqual([])
  })
})

describe('codex degradation settings helpers', () => {
  const available = ['gpt-6.1-sol', 'gpt-6-astra']

  it('validates settings like the backend', () => {
    expect(
      validateDegradationSettings(
        { enabled: true, interval_minutes: 10, block_minutes: 30, models: ['gpt-6.1-sol'] },
        available
      )
    ).toBeNull()
    expect(
      validateDegradationSettings(
        { enabled: true, interval_minutes: 0, block_minutes: 30, models: ['gpt-6.1-sol'] },
        available
      )
    ).toBe('intervalPositive')
    expect(
      validateDegradationSettings(
        { enabled: true, interval_minutes: 10, block_minutes: 0, models: ['gpt-6.1-sol'] },
        available
      )
    ).toBe('blockPositive')
    expect(
      validateDegradationSettings(
        { enabled: true, interval_minutes: 10, block_minutes: 30, models: [] },
        available
      )
    ).toBe('modelsRequired')
    expect(
      validateDegradationSettings(
        { enabled: true, interval_minutes: 10, block_minutes: 30, models: ['removed'] },
        available
      )
    ).toBe('modelsNotAvailable')
  })

  it('normalizes the save payload', () => {
    expect(
      normalizeDegradationSettingsPayload({
        enabled: true,
        interval_minutes: 10.7,
        block_minutes: -3,
        models: [' gpt-6.1-sol ', 'gpt-6.1-sol', 'gpt-6-astra', '']
      })
    ).toEqual({
      enabled: true,
      interval_minutes: 10,
      block_minutes: 1,
      models: ['gpt-6.1-sol', 'gpt-6-astra']
    })
  })

  it('detects the default probe template and trims content', () => {
    expect(isDefaultProbeTemplate(' {"a":1}\n', '{"a":1}')).toBe(true)
    expect(isDefaultProbeTemplate('{"b":2}', '{"a":1}')).toBe(false)
    expect(normalizeProbeTemplate('  {"a":1}\n')).toBe('{"a":1}')
  })
})
