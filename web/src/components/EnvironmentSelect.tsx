import { type ReactElement, useEffect, useState } from 'react'
import { api } from '../api/client'
import type { EnvironmentSummary } from '../api/types'
import { ALL_ENVIRONMENTS, setEnvironment, useEnvironment } from '../environment'
import { getTimeRange } from '../utils/timeRange'
import { sortAlpha } from '../utils/sortAlpha'

/** Picks the environment every environment-aware read is scoped to. */
export function EnvironmentSelect(): ReactElement {
  const selected = useEnvironment()
  const [held, setHeld] = useState<EnvironmentSummary[]>([])

  useEffect(() => {
    const { from, to } = getTimeRange('24h')
    let live = true
    api
      .getEnvironments(from, to)
      .then((rows) => {
        if (live) setHeld(rows)
      })
      .catch(() => {
        // The selector still offers the current choice and "all".
      })
    return () => {
      live = false
    }
  }, [])

  const names = held.map((e) => e.environment)
  if (selected !== ALL_ENVIRONMENTS && !names.includes(selected)) names.push(selected)

  return (
    <label style={{ display: 'flex', flexDirection: 'column', gap: '0.25rem', fontSize: '0.75rem' }}>
      Environment
      <select value={selected} onChange={(e) => setEnvironment(e.target.value)}>
        <option value={ALL_ENVIRONMENTS}>all</option>
        {sortAlpha(names).map((n) => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </select>
    </label>
  )
}
