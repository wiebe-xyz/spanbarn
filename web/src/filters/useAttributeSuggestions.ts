import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api/client'

/** Span columns the filter model reads besides attributes. */
export const COLUMN_KEYS = [
  'service',
  'name',
  'kind',
  'status',
  'resource',
  'duration_us',
  'start_time_us',
  'http_status',
  'trace_id',
  'span_id',
  'parent_span_id',
]

const COLUMN_VALUES: Record<string, string[]> = {
  kind: ['server', 'client', 'internal', 'producer', 'consumer'],
  status: ['ok', 'error', 'unset'],
}

const WINDOW_MS = 24 * 3600_000

export type Suggestions = {
  keys: string[]
  /** Observed values of a key, from attribute discovery. Resolves to [] on failure. */
  loadValues: (key: string) => Promise<string[]>
}

/**
 * Key and value autocomplete for the filter builder, from the attribute
 * discovery endpoint. Keys load once per project. Values load on demand, one
 * request per key, and are cached.
 */
export function useAttributeSuggestions(projectId: number): Suggestions {
  const [attrKeys, setAttrKeys] = useState<string[]>([])
  const valueCache = useRef(new Map<string, string[]>())

  useEffect(() => {
    valueCache.current = new Map()
    if (!projectId) return
    let cancelled = false
    const to = new Date()
    api
      .getAttributes({ projectId, from: new Date(to.getTime() - WINDOW_MS).toISOString(), to: to.toISOString() })
      .then((r) => { if (!cancelled) setAttrKeys((r?.keys ?? []).map((k) => k.key)) })
      .catch(() => { if (!cancelled) setAttrKeys([]) })
    return () => { cancelled = true }
  }, [projectId])

  const loadValues = useCallback(
    async (key: string): Promise<string[]> => {
      const fixed = COLUMN_VALUES[key]
      if (fixed) return fixed
      const cached = valueCache.current.get(key)
      if (cached) return cached
      if (!projectId || !key) return []
      try {
        const to = new Date()
        const r = await api.getAttributes({
          projectId,
          from: new Date(to.getTime() - WINDOW_MS).toISOString(),
          to: to.toISOString(),
          key,
        })
        const values = (r?.keys.find((k) => k.key === key)?.top ?? []).map((v) => v.value)
        valueCache.current.set(key, values)
        return values
      } catch {
        return []
      }
    },
    [projectId],
  )

  return { keys: [...COLUMN_KEYS, ...attrKeys.filter((k) => !COLUMN_KEYS.includes(k))], loadValues }
}
