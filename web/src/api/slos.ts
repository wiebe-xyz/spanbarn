import { fetchJSON } from './client'

export type Slo = {
  id: number
  projectId: number
  name: string
  goodFilter: unknown
  totalFilter: unknown
  /** A fraction between 0 and 1, such as 0.995. */
  target: number
  windowDays: number
  createdAt: string
}

export type SloSettings = {
  name: string
  goodFilter: unknown
  totalFilter: unknown
  target: number
  windowDays: number
}

export type BurnAlert = {
  id: number
  sloId: number
  windowMinutes: number
  burnRate: number
  webhookUrl: string
  email: string
  cooldownMinutes: number
  enabled: boolean
  firing: boolean
}

export type BurnAlertSettings = {
  windowMinutes: number
  burnRate: number
  webhookUrl: string
  email: string
  cooldownMinutes: number
  enabled: boolean
}

export type BurnAlertStatus = {
  id: number
  windowMinutes: number
  burnRate: number
  enabled: boolean
  currentBurn: number
  good: number
  total: number
  firing: boolean
}

export type SloStatus = {
  id: number
  name: string
  target: number
  windowDays: number
  good: number
  total: number
  /** 1 is a full budget, 0 is spent, below 0 is overspent. */
  budgetRemaining: number
  alerts: BurnAlertStatus[] | null
}

const base = (projectId: number) => `project_id=${projectId}`
const json = (method: string, body?: unknown): RequestInit => ({
  method,
  ...(body === undefined ? {} : { body: JSON.stringify(body) }),
})

export const sloApi = {
  list: (projectId: number) => fetchJSON<Slo[]>(`/api/v1/slos?${base(projectId)}`),
  status: (projectId: number, id: number) => fetchJSON<SloStatus>(`/api/v1/slos/${id}/status?${base(projectId)}`),
  create: (projectId: number, s: SloSettings) =>
    fetchJSON<{ id: number }>(`/api/v1/slos?${base(projectId)}`, json('POST', s)),
  update: (projectId: number, id: number, s: SloSettings) =>
    fetchJSON<{ status: string }>(`/api/v1/slos/${id}?${base(projectId)}`, json('PUT', s)),
  remove: (projectId: number, id: number) =>
    fetchJSON<{ status: string }>(`/api/v1/slos/${id}?${base(projectId)}`, json('DELETE')),
  listAlerts: (projectId: number, id: number) =>
    fetchJSON<BurnAlert[]>(`/api/v1/slos/${id}/burn-alerts?${base(projectId)}`),
  createAlert: (projectId: number, id: number, a: BurnAlertSettings) =>
    fetchJSON<{ id: number }>(`/api/v1/slos/${id}/burn-alerts?${base(projectId)}`, json('POST', a)),
  updateAlert: (projectId: number, id: number, alertId: number, a: BurnAlertSettings) =>
    fetchJSON<{ status: string }>(`/api/v1/slos/${id}/burn-alerts/${alertId}?${base(projectId)}`, json('PUT', a)),
  removeAlert: (projectId: number, id: number, alertId: number) =>
    fetchJSON<{ status: string }>(`/api/v1/slos/${id}/burn-alerts/${alertId}?${base(projectId)}`, json('DELETE')),
}

/** The filter for a request body. An empty one matches every span. */
export function filterBody(serialized: string): unknown {
  return serialized ? JSON.parse(serialized) : {}
}
