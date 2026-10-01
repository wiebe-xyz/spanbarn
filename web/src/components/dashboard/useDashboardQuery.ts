import { useEffect, useRef, useState } from 'react'
import type { DashboardFilter } from '../../api/dashboardTypes'

export type DashboardQueryState<T> = {
  data: T | null
  loading: boolean
  error: string | null
}

/**
 * Fetch one card's data whenever `filter` changes (pass a memoised object) or
 * `refreshKey` bumps (auto refresh). Each card owns its request so a failing
 * query leaves the other cards drawn. A counter drops responses from
 * superseded requests.
 */
export function useDashboardQuery<T>(
  filter: DashboardFilter,
  fetcher: (f: DashboardFilter) => Promise<T>,
  refreshKey: number,
): DashboardQueryState<T> {
  const [state, setState] = useState<DashboardQueryState<T>>({ data: null, loading: true, error: null })
  const idRef = useRef(0)
  const fetcherRef = useRef(fetcher)
  useEffect(() => {
    fetcherRef.current = fetcher
  })

  useEffect(() => {
    const id = ++idRef.current
    // eslint-disable-next-line react-hooks/set-state-in-effect -- reset loading when the query changes
    setState((s) => ({ ...s, loading: true, error: null }))
    fetcherRef
      .current(filter)
      .then((data) => {
        if (id === idRef.current) setState({ data, loading: false, error: null })
      })
      .catch((err: unknown) => {
        if (id === idRef.current) {
          setState({ data: null, loading: false, error: err instanceof Error ? err.message : 'Failed to load' })
        }
      })
  }, [filter, refreshKey])

  return state
}
