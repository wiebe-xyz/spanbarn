import { fetchJSON } from './client'

/** A named expression of one project, usable as a filter and group-by key. */
export type CalculatedField = {
  id: number
  projectId: number
  name: string
  expression: string
  createdAt: string
  updatedAt: string
}

/** The value of an expression for one recent span. */
export type CalculatedFieldSample = {
  spanId: string
  /** The span name. */
  name: string
  value: number | string | null
}

export const calculatedFieldsApi = {
  list: (projectId: number) =>
    fetchJSON<CalculatedField[]>(`/api/v1/calculated-fields?project_id=${encodeURIComponent(projectId)}`),

  create: (projectId: number, name: string, expression: string) =>
    fetchJSON<{ id: number }>('/api/v1/calculated-fields', {
      method: 'POST',
      body: JSON.stringify({ projectId, name, expression }),
    }),

  update: (id: number, name: string, expression: string) =>
    fetchJSON<{ status: string }>(`/api/v1/calculated-fields/${id}`, {
      method: 'PUT',
      body: JSON.stringify({ name, expression }),
    }),

  remove: (id: number) =>
    fetchJSON<{ status: string }>(`/api/v1/calculated-fields/${id}`, { method: 'DELETE' }),

  /** Evaluates an unsaved expression on the newest spans. An empty name previews a new field. */
  preview: (projectId: number, name: string, expression: string) =>
    fetchJSON<{ samples: CalculatedFieldSample[] }>('/api/v1/calculated-fields/preview', {
      method: 'POST',
      body: JSON.stringify({ projectId, name, expression }),
    }),
}
