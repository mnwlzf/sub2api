import { describe, expect, it } from 'vitest'
import type { OpenAIBPSCredentialState } from '@/types'
import {
  buildOpenAIBPSCredentials,
  isOpenAIBPSPlatform,
  normalizeOpenAIBPSAccessToken,
  normalizeOpenAIBPSAccountID,
  openAIBPSCredentialStatusDetails,
  openAIBPSCredentialStatusMeta,
  validateOpenAIBPSForm
} from '../openaiBPS'

describe('isOpenAIBPSPlatform', () => {
  it('matches the openai_bps platform value only', () => {
    expect(isOpenAIBPSPlatform('openai_bps')).toBe(true)
    expect(isOpenAIBPSPlatform(' OPENAI_BPS ')).toBe(true)
    expect(isOpenAIBPSPlatform('openai')).toBe(false)
    expect(isOpenAIBPSPlatform('')).toBe(false)
    expect(isOpenAIBPSPlatform(null)).toBe(false)
    expect(isOpenAIBPSPlatform(undefined)).toBe(false)
  })
})

describe('normalizeOpenAIBPSAccessToken', () => {
  it('trims and strips a pasted Bearer prefix', () => {
    expect(normalizeOpenAIBPSAccessToken('  Bearer abc.def  ')).toBe('abc.def')
    expect(normalizeOpenAIBPSAccessToken('bearer xyz')).toBe('xyz')
    expect(normalizeOpenAIBPSAccessToken('  token  ')).toBe('token')
  })

  it('treats the redaction placeholder and non-strings as empty', () => {
    expect(normalizeOpenAIBPSAccessToken('***')).toBe('')
    expect(normalizeOpenAIBPSAccessToken(42)).toBe('')
    expect(normalizeOpenAIBPSAccessToken(null)).toBe('')
  })
})

describe('validateOpenAIBPSForm', () => {
  it('requires the token on create', () => {
    expect(validateOpenAIBPSForm({ accessToken: '' }, 'create')).toBe('tokenRequired')
    expect(validateOpenAIBPSForm({ accessToken: '   ' }, 'create')).toBe('tokenRequired')
    expect(validateOpenAIBPSForm({ accessToken: 'token' }, 'create')).toBeNull()
  })

  it('allows an empty token on edit (keep current token)', () => {
    expect(validateOpenAIBPSForm({ accessToken: '' }, 'edit')).toBeNull()
    expect(validateOpenAIBPSForm({ accessToken: '***' }, 'edit')).toBeNull()
  })

  it('rejects whitespace inside the optional account id', () => {
    expect(validateOpenAIBPSForm({ accessToken: 'token', chatgptAccountId: 'a b' }, 'create')).toBe(
      'accountIdInvalid'
    )
    expect(validateOpenAIBPSForm({ accessToken: 'token', chatgptAccountId: 'acct-1' }, 'create')).toBeNull()
  })
})

describe('buildOpenAIBPSCredentials', () => {
  it('builds access_token + optional chatgpt_account_id', () => {
    expect(
      buildOpenAIBPSCredentials({ accessToken: ' Bearer abc ', chatgptAccountId: ' acct-1 ' })
    ).toEqual({ access_token: 'abc', chatgpt_account_id: 'acct-1' })
  })

  it('omits an empty token so edits keep the stored token (sensitive-key merge)', () => {
    expect(buildOpenAIBPSCredentials({ accessToken: '', chatgptAccountId: '' })).toEqual({})
  })

  it('omits an empty account id so edits clear it (non-sensitive full replacement)', () => {
    expect(buildOpenAIBPSCredentials({ accessToken: 'token', chatgptAccountId: '   ' })).toEqual({
      access_token: 'token'
    })
  })
})

describe('openAIBPSCredentialStatusMeta', () => {
  it('returns null for non-BPS platforms or missing state', () => {
    expect(openAIBPSCredentialStatusMeta('openai', { status: 'revoked' })).toBeNull()
    expect(openAIBPSCredentialStatusMeta('openai_bps', null)).toBeNull()
    expect(openAIBPSCredentialStatusMeta('openai_bps', { status: 'weird' as never })).toBeNull()
  })

  it.each([
    ['normal', false],
    ['expired', true],
    ['revoked', true],
    ['auth_failed', true]
  ] as const)('maps %s to a badge with abnormal=%s', (status, abnormal) => {
    const state: OpenAIBPSCredentialState = { status }
    const meta = openAIBPSCredentialStatusMeta('openai_bps', state)
    expect(meta).not.toBeNull()
    expect(meta?.status).toBe(status)
    expect(meta?.labelKey).toBe(status)
    expect(meta?.abnormal).toBe(abnormal)
    expect(meta?.badgeClass).toBeTruthy()
    expect(meta?.dotClass).toBeTruthy()
  })
})

describe('openAIBPSCredentialStatusDetails', () => {
  it('extracts optional expiry / last-seen / error code', () => {
    expect(
      openAIBPSCredentialStatusDetails({
        status: 'expired',
        expires_at: '2026-01-01T00:00:00Z',
        last_seen_at: '2026-01-02T00:00:00Z',
        error_code: 'bps_token_expired'
      })
    ).toEqual({
      expiresAt: '2026-01-01T00:00:00Z',
      lastSeenAt: '2026-01-02T00:00:00Z',
      errorCode: 'bps_token_expired'
    })
  })

  it('falls back to empty strings', () => {
    expect(openAIBPSCredentialStatusDetails(undefined)).toEqual({
      expiresAt: '',
      lastSeenAt: '',
      errorCode: ''
    })
  })
})
