/**
 * Admin Pelican Probe API endpoints
 * Group-level IQ/degradation monitor with human review
 */

import { apiClient } from '../client'
import type {
  PelicanProbePlan,
  PelicanProbeResult,
  PelicanProbeGlobalStatus,
  CreatePelicanProbePlanRequest,
  UpdatePelicanProbePlanRequest,
  ReviewPelicanProbeResultRequest
} from '@/types'

/**
 * List probe plans, optionally filtered by group
 */
export async function listPlans(groupId?: number): Promise<PelicanProbePlan[]> {
  const { data } = await apiClient.get<PelicanProbePlan[]>('/admin/pelican-probe-plans', {
    params: groupId ? { group_id: groupId } : undefined
  })
  return data ?? []
}

/**
 * Create a new probe plan
 */
export async function createPlan(req: CreatePelicanProbePlanRequest): Promise<PelicanProbePlan> {
  const { data } = await apiClient.post<PelicanProbePlan>('/admin/pelican-probe-plans', req)
  return data
}

/**
 * Update a probe plan (api_key empty = keep existing)
 */
export async function updatePlan(
  id: number,
  req: UpdatePelicanProbePlanRequest
): Promise<PelicanProbePlan> {
  const { data } = await apiClient.put<PelicanProbePlan>(`/admin/pelican-probe-plans/${id}`, req)
  return data
}

/**
 * Delete a probe plan and its results
 */
export async function deletePlan(id: number): Promise<void> {
  await apiClient.delete(`/admin/pelican-probe-plans/${id}`)
}

/**
 * New async contract: the endpoint answers HTTP 202 immediately and the probe
 * keeps running in the background (up to 10 minutes).
 */
export interface PelicanProbeRunStartResponse {
  started: boolean
  plan_id: number
}

/**
 * Legacy contract returned the finished result inline; the async contract
 * returns only the acknowledgement. Callers must treat any 2xx as "started"
 * and poll the results list instead of reading the body.
 */
export type PelicanProbeRunResponse = PelicanProbeResult | PelicanProbeRunStartResponse

/**
 * Trigger an immediate probe run for a plan.
 *
 * The response body is intentionally typed as a union and ignored by callers:
 * whichever backend contract is deployed (legacy synchronous result or new
 * asynchronous 202 acknowledgement), any 2xx means the probe was launched.
 */
export async function runNow(id: number): Promise<PelicanProbeRunResponse> {
  const { data } = await apiClient.post<PelicanProbeRunResponse>(
    `/admin/pelican-probe-plans/${id}/run`
  )
  return data
}

/**
 * List results for a plan
 */
export async function listResults(planId: number, limit?: number): Promise<PelicanProbeResult[]> {
  const { data } = await apiClient.get<PelicanProbeResult[]>(
    `/admin/pelican-probe-plans/${planId}/results`,
    { params: limit ? { limit } : undefined }
  )
  return data ?? []
}

/**
 * List recent results across plans, optionally filtered by group
 */
export async function listRecent(
  groupId?: number,
  limit?: number
): Promise<PelicanProbeResult[]> {
  const { data } = await apiClient.get<PelicanProbeResult[]>('/admin/pelican-probe-results', {
    params: { ...(groupId ? { group_id: groupId } : {}), ...(limit ? { limit } : {}) }
  })
  return data ?? []
}

/**
 * Record a human verdict on a result
 */
export async function reviewResult(
  id: number,
  req: ReviewPelicanProbeResultRequest
): Promise<PelicanProbeResult> {
  const { data } = await apiClient.post<PelicanProbeResult>(
    `/admin/pelican-probe-results/${id}/review`,
    req
  )
  return data
}

/**
 * Get the global on/off switch
 */
export async function getGlobalStatus(): Promise<PelicanProbeGlobalStatus> {
  const { data } = await apiClient.get<PelicanProbeGlobalStatus>('/admin/pelican-probe-status')
  return data
}

/**
 * Set the global on/off switch
 */
export async function setGlobalStatus(enabled: boolean): Promise<PelicanProbeGlobalStatus> {
  const { data } = await apiClient.put<PelicanProbeGlobalStatus>('/admin/pelican-probe-status', {
    enabled
  })
  return data
}

export const pelicanProbeAPI = {
  listPlans,
  createPlan,
  updatePlan,
  deletePlan,
  runNow,
  listResults,
  listRecent,
  reviewResult,
  getGlobalStatus,
  setGlobalStatus
}

export default pelicanProbeAPI
