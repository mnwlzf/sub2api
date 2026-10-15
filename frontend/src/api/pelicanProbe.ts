/**
 * User-facing Pelican Probe API endpoints (read-only)
 */

import { apiClient } from './client'
import type { PelicanProbePublicStatus } from '@/types'

/**
 * Get the public IQ probe status (global switch + public plans + recent results)
 */
export async function getPublicStatus(): Promise<PelicanProbePublicStatus> {
  const { data } = await apiClient.get<PelicanProbePublicStatus>('/user/pelican-probe/status')
  return data
}

export const pelicanProbeUserAPI = {
  getPublicStatus
}

export default pelicanProbeUserAPI
