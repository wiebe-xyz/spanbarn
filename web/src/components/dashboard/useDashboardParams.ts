import { useCallback } from 'react'
import { useSearchParams } from 'react-router-dom'
import type { CountsGroup } from '../../api/dashboardTypes'
import { DEFAULT_DASHBOARD_RANGE, findDashboardRange } from '../../utils/dashboardWindow'
import type { DashboardFilterValues } from './DashboardFilters'

/** Dimensions the counts card can group by and filter on. */
export const COUNTS_GROUPS: { value: CountsGroup; label: string }[] = [
  { value: 'service', label: 'Service' },
  { value: 'name', label: 'Span name' },
  { value: 'status', label: 'Span status' },
]

export type DashboardState = {
  range: string
  offset: number
  /** Raw epoch-ms params of a zoomed window; null on a preset range. */
  customFrom: string | null
  customTo: string | null
  filters: DashboardFilterValues
  minUs: number
  maxUs: number
  countsGroup: CountsGroup
}

type Changes = Record<string, string | number | undefined>

function nonNegative(v: string | null): number {
  return Math.max(0, Number(v) || 0)
}

export function readDashboardState(params: URLSearchParams): DashboardState {
  const group = params.get('cg')
  return {
    range: findDashboardRange(params.get('range') ?? DEFAULT_DASHBOARD_RANGE).value,
    offset: nonNegative(params.get('offset')),
    customFrom: params.get('from'),
    customTo: params.get('to'),
    filters: {
      projectId: Number(params.get('project')) || 0,
      service: params.get('service') ?? '',
      name: params.get('name') ?? '',
      status: params.get('status') ?? '',
    },
    minUs: nonNegative(params.get('min_us')),
    maxUs: nonNegative(params.get('max_us')),
    countsGroup: COUNTS_GROUPS.some((g) => g.value === group) ? (group as CountsGroup) : 'service',
  }
}

/**
 * The dashboard's view lives in the URL so it can be linked and survives a
 * reload. Zoom pushes a history entry so the browser's Back button undoes it;
 * every other change replaces the current one.
 */
export function useDashboardParams() {
  const [params, setParams] = useSearchParams()
  const update = useCallback(
    (changes: Changes, push = false) => {
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          for (const [k, v] of Object.entries(changes)) {
            if (v === undefined || v === '' || v === 0) next.delete(k)
            else next.set(k, String(v))
          }
          return next
        },
        { replace: !push },
      )
    },
    [setParams],
  )
  return { state: readDashboardState(params), update }
}
