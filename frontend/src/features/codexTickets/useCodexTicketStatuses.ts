/**
 * 账号列表「打票」列的共享状态缓存。
 *
 * 后端只提供按账号查询票据摘要的接口，列表页一屏最多 N 行；这里做三件事：
 *   1. 30s 缓存，避免翻页/重渲染反复打接口；
 *   2. 最多 4 个并发，避免一屏 20 行同时打爆管理端；
 *   3. 暴露 per-account loading/error，供单元格展示。
 *
 * 缓存是模块级单例（同一页面内所有单元格共享），组件卸载不需要清理。
 */

import { reactive } from 'vue'
import { adminAPI } from '@/api/admin'
import type { CodexTicketStatusesResponse } from '@/api/admin/codexTicket'

const CACHE_TTL_MS = 30_000
const MAX_CONCURRENT = 4

interface CacheEntry {
  data: CodexTicketStatusesResponse
  ts: number
}

const cache = reactive<Record<string, CacheEntry>>({})
const loading = reactive<Record<string, boolean>>({})
const errors = reactive<Record<string, string>>({})
const inFlight = new Map<number, Promise<CodexTicketStatusesResponse>>()

const queue: Array<{
  accountId: number
  resolve: (value: CodexTicketStatusesResponse) => void
  reject: (reason: unknown) => void
}> = []
let activeCount = 0

function isFresh(entry: CacheEntry | undefined): entry is CacheEntry {
  return !!entry && Date.now() - entry.ts < CACHE_TTL_MS
}

function pump(): void {
  while (activeCount < MAX_CONCURRENT && queue.length > 0) {
    const job = queue.shift()
    if (!job) return
    activeCount += 1
    void (async () => {
      try {
        const data = await adminAPI.codexTicket.getAccountTicketStatuses(job.accountId)
        cache[String(job.accountId)] = { data, ts: Date.now() }
        delete errors[String(job.accountId)]
        job.resolve(data)
      } catch (error) {
        const message =
          typeof error === 'object' && error !== null && 'message' in error
            ? String((error as { message?: unknown }).message ?? '')
            : ''
        errors[String(job.accountId)] = message || 'request_failed'
        job.reject(error)
      } finally {
        delete loading[String(job.accountId)]
        activeCount -= 1
        pump()
      }
    })()
  }
}

function enqueue(accountId: number): Promise<CodexTicketStatusesResponse> {
  const existing = inFlight.get(accountId)
  if (existing) return existing
  loading[String(accountId)] = true
  const promise = new Promise<CodexTicketStatusesResponse>((resolve, reject) => {
    queue.push({ accountId, resolve, reject })
    pump()
  })
  inFlight.set(accountId, promise)
  const cleanup = () => {
    inFlight.delete(accountId)
  }
  promise.then(cleanup, cleanup)
  return promise
}

/** 读取缓存（不触发请求）；无缓存返回 null。 */
export function getCachedTicketStatuses(
  accountId: number
): CodexTicketStatusesResponse | null {
  const entry = cache[String(accountId)]
  return isFresh(entry) ? entry.data : null
}

export function isTicketStatusesLoading(accountId: number): boolean {
  return loading[String(accountId)] === true
}

export function getTicketStatusesError(accountId: number): string | null {
  return errors[String(accountId)] ?? null
}

/** 强制失效某账号（或全部）缓存，下次读取会重新请求。 */
export function invalidateTicketStatuses(accountId?: number): void {
  if (accountId == null) {
    for (const key of Object.keys(cache)) delete cache[key]
    return
  }
  delete cache[String(accountId)]
}

/**
 * 确保某账号的票据摘要已加载。
 * @param force 忽略缓存重新请求（手动打票/参与开关保存后使用）
 */
export async function ensureTicketStatuses(
  accountId: number,
  options: { force?: boolean } = {}
): Promise<CodexTicketStatusesResponse | null> {
  if (!options.force) {
    const cached = getCachedTicketStatuses(accountId)
    if (cached) return cached
  }
  try {
    return await enqueue(accountId)
  } catch {
    return null
  }
}

/** 仅测试使用：清空模块级缓存与队列状态。 */
export function __resetCodexTicketStatusesCacheForTests(): void {
  for (const key of Object.keys(cache)) delete cache[key]
  for (const key of Object.keys(loading)) delete loading[key]
  for (const key of Object.keys(errors)) delete errors[key]
  inFlight.clear()
  queue.length = 0
  activeCount = 0
}
