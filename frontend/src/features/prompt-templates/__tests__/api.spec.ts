import { beforeEach, describe, expect, it, vi } from 'vitest'

const client = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: client }))

import { listRequestEvents } from '../api'
import type { PromptRequestEvent } from '../types'

function event(id: number): PromptRequestEvent {
  return {
    id,
    attempt_no: 1,
    applied: true,
    reason: 'applied',
    added_bytes: 111,
    apply_duration_ms: 3,
    created_at: '2026-10-08T06:00:00Z',
  }
}

describe('prompt request events API', () => {
  beforeEach(() => Object.values(client).forEach((mock) => mock.mockReset()))

  it('keeps the legacy array contract when only limit is passed', async () => {
    const events = [event(1), event(2)]
    client.get.mockResolvedValue({ data: events })

    const result = await listRequestEvents({ limit: 100 })

    expect(client.get).toHaveBeenCalledWith('/admin/prompt-request-events', {
      params: { limit: 100 },
    })
    expect(result).toEqual(events)
  })

  it('sends page/page_size and returns the pagination envelope unchanged', async () => {
    const envelope = { items: [event(3)], total: 41, page: 2, page_size: 20, pages: 3 }
    client.get.mockResolvedValue({ data: envelope })

    const result = await listRequestEvents({ group_id: 2, applied: true, page: 2, page_size: 20 })

    expect(client.get).toHaveBeenCalledWith('/admin/prompt-request-events', {
      params: { group_id: 2, applied: true, page: 2, page_size: 20 },
    })
    expect(result).toEqual(envelope)
  })

  it('defaults to an unfiltered legacy query', async () => {
    client.get.mockResolvedValue({ data: [] })

    await listRequestEvents()

    expect(client.get).toHaveBeenCalledWith('/admin/prompt-request-events', { params: {} })
  })
})
