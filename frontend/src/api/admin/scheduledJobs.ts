import { apiClient } from '../client'
import type {
  AdminScheduledJob,
  AdminScheduledJobRun,
  CreateAdminScheduledJobRequest,
  UpdateAdminScheduledJobRequest,
} from '@/types'

export async function list(): Promise<AdminScheduledJob[]> {
  const { data } = await apiClient.get<AdminScheduledJob[]>('/admin/scheduled-jobs')
  return data ?? []
}

export async function get(id: number): Promise<AdminScheduledJob> {
  const { data } = await apiClient.get<AdminScheduledJob>(`/admin/scheduled-jobs/${id}`)
  return data
}

export async function create(req: CreateAdminScheduledJobRequest): Promise<AdminScheduledJob> {
  const { data } = await apiClient.post<AdminScheduledJob>('/admin/scheduled-jobs', req)
  return data
}

export async function update(id: number, req: UpdateAdminScheduledJobRequest): Promise<AdminScheduledJob> {
  const { data } = await apiClient.put<AdminScheduledJob>(`/admin/scheduled-jobs/${id}`, req)
  return data
}

export async function deleteJob(id: number): Promise<void> {
  await apiClient.delete(`/admin/scheduled-jobs/${id}`)
}

export async function runNow(id: number): Promise<AdminScheduledJobRun> {
  const { data } = await apiClient.post<AdminScheduledJobRun>(`/admin/scheduled-jobs/${id}/run`)
  return data
}

export async function listRuns(id: number, limit = 50): Promise<AdminScheduledJobRun[]> {
  const { data } = await apiClient.get<AdminScheduledJobRun[]>(`/admin/scheduled-jobs/${id}/runs`, {
    params: { limit }
  })
  return data ?? []
}

export const scheduledJobsAPI = {
  list,
  get,
  create,
  update,
  delete: deleteJob,
  runNow,
  listRuns,
}

export default scheduledJobsAPI
