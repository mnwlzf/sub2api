import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({
  post: vi.fn()
}))

vi.mock('@/api/client', () => ({
  apiClient: { post }
}))

import { probePoolUpstreamInfo } from '@/api/admin/accounts'

describe('admin account pool upstream info API', () => {
  beforeEach(() => {
    post.mockReset()
  })

  it('posts to the per-account manual refresh endpoint', async () => {
    const result = {
      account_id: 7,
      snapshot: {
        status: 'ok',
        platform: 'sub2api',
        features: ['balance'],
        data: { kind: 'wallet', amount_usd: 12.5 }
      }
    }
    post.mockResolvedValueOnce({ data: result })

    await expect(probePoolUpstreamInfo(7)).resolves.toEqual(result)
    expect(post).toHaveBeenCalledWith('/admin/accounts/7/pool-upstream-info-probe')
  })
})
