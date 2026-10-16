import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ProbeResultBody from '../ProbeResultBody.vue'
import type { PelicanProbeResult } from '@/types'

const TRANSLATIONS: Record<string, string> = {
  'admin.pelicanProbe.statusFailed': 'Failed',
  'admin.pelicanProbe.verdictPass': 'Pass',
  'admin.pelicanProbe.verdictDegraded': 'Degraded',
  'admin.pelicanProbe.verdictPending': 'Pending',
  'admin.pelicanProbe.candyAnswerLabel': 'Model answer',
  'admin.pelicanProbe.candyVerdictPass': 'Judged 21 ✓',
  'admin.pelicanProbe.candyVerdictDegraded': 'Not judged 21 ✗',
  'admin.pelicanProbe.candyVerdictPending': 'Awaiting judge / review',
  'admin.pelicanProbe.resultUnavailable': 'This probe did not produce a valid result'
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => TRANSLATIONS[key] ?? key
    })
  }
})

function makeResult(overrides: Partial<PelicanProbeResult> = {}): PelicanProbeResult {
  return {
    id: 1,
    plan_id: 1,
    status: 'success',
    raw_html: '答案是 21',
    verdict: 'pending',
    note: '',
    reviewed_by: '',
    reviewed_at: null,
    error_message: '',
    latency_ms: 1000,
    prompt_tokens: 1,
    completion_tokens: 1,
    started_at: '2026-03-15T00:00:00Z',
    finished_at: '2026-03-15T00:00:01Z',
    created_at: '2026-03-15T00:00:00Z',
    ...overrides
  }
}

describe('ProbeResultBody (admin results wall)', () => {
  it('renders a candy answer as escaped plain text, never as an iframe', () => {
    const wrapper = mount(ProbeResultBody, {
      props: { result: makeResult({ raw_html: '<b>21</b>' }), testType: 'candy' }
    })

    expect(wrapper.find('iframe').exists()).toBe(false)
    const pre = wrapper.find('pre')
    expect(pre.exists()).toBe(true)
    expect(pre.text()).toBe('<b>21</b>')
    // The markup must be text, not parsed nodes.
    expect(pre.find('b').exists()).toBe(false)
  })

  it('labels the candy verdict for pass, degraded and pending', () => {
    const label = (verdict: string) =>
      mount(ProbeResultBody, {
        props: { result: makeResult({ verdict }), testType: 'candy' }
      })
        .find('div.absolute')
        .text()

    expect(label('pass')).toBe('Judged 21 ✓')
    expect(label('degraded')).toBe('Not judged 21 ✗')
    expect(label('pending')).toBe('Awaiting judge / review')
  })

  it('shows the judge note under the candy answer', () => {
    const wrapper = mount(ProbeResultBody, {
      props: {
        result: makeResult({ raw_html: '21', verdict: 'pass', note: 'judge=judge-model: YES' }),
        testType: 'candy'
      }
    })
    expect(wrapper.text()).toContain('judge=judge-model: YES')
  })

  it('keeps the answer, the label and the judge note on the admin wall', () => {
    const wrapper = mount(ProbeResultBody, {
      props: {
        result: makeResult({
          raw_html: '我先想一下……答案是 21',
          verdict: 'degraded',
          note: 'judge=deepseek-v4.1-flash: NO'
        }),
        testType: 'candy',
        variant: 'admin'
      }
    })

    expect(wrapper.find('pre').text()).toBe('我先想一下……答案是 21')
    expect(wrapper.text()).toContain('Model answer')
    expect(wrapper.text()).toContain('judge=deepseek-v4.1-flash: NO')
  })

  it('keeps the sandboxed iframe preview for pelican results', () => {
    const wrapper = mount(ProbeResultBody, {
      props: { result: makeResult({ raw_html: '<svg/>' }), testType: 'pelican' }
    })

    const iframe = wrapper.find('iframe')
    expect(iframe.exists()).toBe(true)
    expect(iframe.attributes('srcdoc')).toBe('<svg/>')
    expect(iframe.attributes('sandbox')).toBe('allow-scripts')
    expect(iframe.attributes('title')).toBe('pelican probe preview')
    expect(wrapper.find('pre').exists()).toBe(false)
  })

  it('falls back to result.test_type when the plan type is unknown', () => {
    const wrapper = mount(ProbeResultBody, {
      props: {
        result: { ...makeResult({ raw_html: '21' }), test_type: 'candy' } as PelicanProbeResult
      }
    })
    expect(wrapper.find('iframe').exists()).toBe(false)
    expect(wrapper.find('pre').exists()).toBe(true)
  })

  it('shows the raw error to admins and a human sentence to users', () => {
    const failed = makeResult({
      status: 'failed',
      error_message: 'Post "http://127.0.0.1:8080/v1/chat/completions": context canceled'
    })

    const admin = mount(ProbeResultBody, { props: { result: failed } })
    expect(admin.text()).toContain('127.0.0.1:8080')

    const user = mount(ProbeResultBody, { props: { result: failed, variant: 'user' } })
    expect(user.text()).not.toContain('127.0.0.1')
    expect(user.text()).toContain('This probe did not produce a valid result')
  })
})

describe('ProbeResultBody (user-facing pages)', () => {
  it('shows the candy verdict only — no answer, no label, no judge note', () => {
    const wrapper = mount(ProbeResultBody, {
      props: {
        result: makeResult({
          raw_html: '我先想一下……答案是 21',
          verdict: 'degraded',
          note: 'judge=deepseek-v4.1-flash: NO'
        }),
        testType: 'candy',
        variant: 'user'
      }
    })

    expect(wrapper.text()).toContain('Not judged 21 ✗')
    expect(wrapper.text()).not.toContain('答案是 21')
    expect(wrapper.text()).not.toContain('Model answer')
    expect(wrapper.text()).not.toContain('deepseek-v4.1-flash')
    expect(wrapper.find('pre').exists()).toBe(false)
    expect(wrapper.find('iframe').exists()).toBe(false)
  })

  it('renders the pelican document and nothing else', () => {
    const wrapper = mount(ProbeResultBody, {
      props: {
        result: makeResult({
          raw_html: '<!DOCTYPE html><html><body>bike</body></html>',
          note: '脚踩空',
          verdict: 'pass'
        }),
        testType: 'pelican',
        variant: 'user',
        showVerdict: false,
        previewTitle: 'pelican probe history preview'
      }
    })

    const iframe = wrapper.find('iframe')
    expect(iframe.exists()).toBe(true)
    expect(iframe.attributes('srcdoc')).toBe('<!DOCTYPE html><html><body>bike</body></html>')
    expect(iframe.attributes('title')).toBe('pelican probe history preview')
    expect(wrapper.find('pre').exists()).toBe(false)
    expect(wrapper.text()).toBe('')
  })

  it('never shows any note to users, human or diagnostic', () => {
    const user = (note: string) =>
      mount(ProbeResultBody, {
        props: { result: makeResult({ note }), testType: 'pelican', variant: 'user' }
      }).text()

    expect(user('auto-judge skipped: judge HTTP 404: judge boom')).not.toContain('auto-judge skipped')
    expect(user('Post "http://127.0.0.1:8080/v1/chat/completions": canceled')).not.toContain('127.0.0.1')
    expect(user('脚踩空')).not.toContain('脚踩空')
  })
})
