import type { VersionSummary } from '../api/types'

/** Change of the p95 against the next older version, as "+12%", or "" without a baseline. */
export function p95Change(current: VersionSummary, previous: VersionSummary | undefined): string {
  if (!previous || previous.p95Us <= 0) return ''
  const pct = Math.round(((current.p95Us - previous.p95Us) / previous.p95Us) * 100)
  return `${pct > 0 ? '+' : ''}${pct}%`
}
