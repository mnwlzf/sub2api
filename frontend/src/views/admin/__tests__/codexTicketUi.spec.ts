import { describe, expect, it } from 'vitest'

import type { CodexTicketStatus } from '@/api/admin/codexTicket'
import {
  buildParticipationFromStatuses,
  classifyTicketStatus,
  countTicketStates,
  formatTicketRemainingSeconds,
  isCodexTicketEligibleAccount,
  isTicketLengthExpected,
  manualHarvestBlockedReason,
  normalizeTicketStatuses,
  readAccountTicketParticipationEnabled,
  readAccountTicketParticipationModels,
  ticketStateI18nKey,
  ticketSummaryI18nKey
} from '@/features/codexTickets/ticketStatus'
import {
  buildTicketProxyPoolPayload,
  normalizeTicketCadence,
  ticketProxyPoolError,
  validateTicketCadence
} from '@/features/codexTickets/settings'

function makeStatus(overrides: Partial<CodexTicketStatus> = {}): CodexTicketStatus {
  return {
    model: 'gpt-6.1-sol',
    length: 292,
    turn_state_present: true,
    cookie_present: true,
    ready: true,
    blocked: false,
    remaining_seconds: 1800,
    expected_ticket_length: 292,
    harvest_enabled: true,
    harvest_paused: false,
    participation_enabled: true,
    ...overrides
  }
}

describe('codex ticket status classification', () => {
  it('classifies a usable ticket with enough TTL as ready', () => {
    expect(classifyTicketStatus(makeStatus())).toBe('ready')
  })

  it('classifies a usable ticket below the refresh threshold as expiring', () => {
    expect(classifyTicketStatus(makeStatus({ remaining_seconds: 300 }))).toBe('expiring')
  })

  it('classifies an existing but unusable ticket as expired', () => {
    expect(
      classifyTicketStatus(
        makeStatus({ ready: false, captured_at: '2026-01-01T00:00:00Z', expires_at: '2026-01-01T01:00:00Z' })
      )
    ).toBe('expired')
  })

  it('classifies a never-captured ticket as missing', () => {
    expect(classifyTicketStatus(makeStatus({ ready: false, remaining_seconds: 0 }))).toBe('missing')
  })

  it('prioritises blocked / paused / disabled / not-participating states', () => {
    expect(classifyTicketStatus(makeStatus({ ready: false, blocked: true }))).toBe('blocked')
    expect(classifyTicketStatus(makeStatus({ ready: false, harvest_paused: true }))).toBe('paused')
    expect(classifyTicketStatus(makeStatus({ harvest_enabled: false }))).toBe('disabled')
    expect(classifyTicketStatus(makeStatus({ participation_enabled: false }))).toBe(
      'not_participating'
    )
  })

  it('exposes i18n keys per state', () => {
    expect(ticketStateI18nKey('ready')).toBe('admin.codex.ticket.state.ready')
    expect(ticketStateI18nKey('not_participating')).toBe(
      'admin.codex.ticket.state.not_participating'
    )
  })

  it('detects accounts eligible for ticket management', () => {
    expect(isCodexTicketEligibleAccount({ platform: 'openai', type: 'oauth' })).toBe(true)
    expect(isCodexTicketEligibleAccount({ platform: 'openai', type: 'setup-token' })).toBe(true)
    expect(
      isCodexTicketEligibleAccount({ platform: 'openai', type: 'oauth', parent_account_id: 9 })
    ).toBe(false)
    expect(isCodexTicketEligibleAccount({ platform: 'openai', type: 'apikey' })).toBe(false)
    expect(isCodexTicketEligibleAccount({ platform: 'anthropic', type: 'oauth' })).toBe(false)
    expect(isCodexTicketEligibleAccount(null)).toBe(false)
  })

  it('normalizes status lists by dropping empty models and null items', () => {
    const response = {
      account_id: 1,
      items: [makeStatus(), makeStatus({ model: '  ' }), null as unknown as CodexTicketStatus],
      manual_available: true,
      manual_unavailable_reason: ''
    }
    expect(normalizeTicketStatuses(response)).toHaveLength(1)
    expect(normalizeTicketStatuses(null)).toEqual([])
  })

  it('counts states and produces a column summary key', () => {
    const counts = countTicketStates([makeStatus(), makeStatus({ model: 'gpt-6-astra' })])
    expect(counts.total).toBe(2)
    expect(counts.ready).toBe(2)
    expect(counts.allReady).toBe(true)
    expect(counts.hasProblem).toBe(false)
    expect(ticketSummaryI18nKey(counts)).toBe('admin.codex.ticket.summary.allReady')

    const problemCounts = countTicketStates([
      makeStatus(),
      makeStatus({ model: 'gpt-6-astra', ready: false, blocked: true })
    ])
    expect(problemCounts.hasProblem).toBe(true)
    expect(ticketSummaryI18nKey(problemCounts)).toBe('admin.codex.ticket.summary.blocked')
    expect(ticketSummaryI18nKey(countTicketStates([]))).toBeNull()
  })

  it('formats remaining seconds compactly', () => {
    expect(formatTicketRemainingSeconds(0)).toBe('0s')
    expect(formatTicketRemainingSeconds(59)).toBe('59s')
    expect(formatTicketRemainingSeconds(600)).toBe('10m')
    expect(formatTicketRemainingSeconds(3600)).toBe('1h')
    expect(formatTicketRemainingSeconds(3660)).toBe('1h 1m')
    expect(formatTicketRemainingSeconds(90000)).toBe('1d 1h')
    expect(formatTicketRemainingSeconds(Number.NaN)).toBe('0s')
  })

  it('compares ticket length with the account expected length', () => {
    expect(isTicketLengthExpected({ length: 292, expected_ticket_length: 292 })).toBe(true)
    expect(isTicketLengthExpected({ length: 291, expected_ticket_length: 292 })).toBe(false)
    expect(isTicketLengthExpected({ length: 0, expected_ticket_length: 0 })).toBe(true)
  })

  it('builds participation state from statuses and account extra', () => {
    const statuses = [
      makeStatus(),
      makeStatus({ model: 'gpt-6-astra', participation_enabled: false })
    ]
    expect(buildParticipationFromStatuses(statuses, false)).toEqual({
      enabled: false,
      models: { 'gpt-6.1-sol': true, 'gpt-6-astra': false }
    })
    expect(readAccountTicketParticipationEnabled({ codex_ticket_harvest_enabled: false })).toBe(false)
    expect(readAccountTicketParticipationEnabled({})).toBe(true)
    expect(readAccountTicketParticipationEnabled(null)).toBe(true)
    expect(
      readAccountTicketParticipationModels({ codex_ticket_harvest_models: { 'gpt-6-astra': false } })
    ).toEqual({ 'gpt-6-astra': false })
    expect(readAccountTicketParticipationModels({ codex_ticket_harvest_models: [] })).toEqual({})
  })

  it('reports why manual harvesting is blocked', () => {
    expect(manualHarvestBlockedReason({ manual_available: true, manual_unavailable_reason: '' })).toBe('')
    expect(manualHarvestBlockedReason({ manual_available: false, manual_unavailable_reason: 'disabled' })).toBe('disabled')
    expect(manualHarvestBlockedReason(null)).toBe('unavailable')
  })
})

describe('codex ticket settings helpers', () => {
  it('validates cadence values', () => {
    expect(validateTicketCadence({ retry_min_seconds: 10, retry_max_seconds: 30, refresh_seconds: 1800 })).toBeNull()
    expect(validateTicketCadence({ retry_min_seconds: 0, retry_max_seconds: 30, refresh_seconds: 1800 })).toBe('retryMinPositive')
    expect(validateTicketCadence({ retry_min_seconds: 10, retry_max_seconds: 0, refresh_seconds: 1800 })).toBe('retryMaxPositive')
    expect(validateTicketCadence({ retry_min_seconds: 10, retry_max_seconds: 30, refresh_seconds: 0 })).toBe('refreshPositive')
    expect(validateTicketCadence({ retry_min_seconds: 60, retry_max_seconds: 30, refresh_seconds: 1800 })).toBe('retryMinGreaterThanMax')
  })

  it('normalizes cadence to positive integers', () => {
    expect(normalizeTicketCadence({ retry_min_seconds: 10.9, retry_max_seconds: -1, refresh_seconds: 0 })).toEqual({
      retry_min_seconds: 10,
      retry_max_seconds: 1,
      refresh_seconds: 1
    })
  })

  it('builds proxy pool payloads', () => {
    expect(buildTicketProxyPoolPayload('all', [3, 1])).toEqual({ mode: 'all', proxy_ids: [] })
    expect(buildTicketProxyPoolPayload('custom', [3, 1, 3, 0, -2])).toEqual({
      mode: 'custom',
      proxy_ids: [1, 3]
    })
    expect(ticketProxyPoolError({ mode: 'custom', proxy_ids: [] })).toBe('noProxySelected')
    expect(ticketProxyPoolError({ mode: 'all', proxy_ids: [] })).toBeNull()
    expect(ticketProxyPoolError({ mode: 'custom', proxy_ids: [1] })).toBeNull()
  })
})
