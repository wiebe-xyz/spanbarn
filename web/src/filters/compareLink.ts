import { serializeFilter, type FilterExpr } from './model'

/** Ranges the comparison page offers. The server caps the range at 7 days. */
export const COMPARE_RANGES = [
  { value: '1h', label: 'Last 1 hour', hours: 1 },
  { value: '24h', label: 'Last 1 day', hours: 24 },
  { value: '48h', label: 'Last 2 days', hours: 48 },
  { value: '7d', label: 'Last 7 days', hours: 168 },
] as const

/** The smallest offered range that reaches back to `fromMs`, or the widest one. */
export function rangeCovering(fromMs: number, nowMs: number = Date.now()): (typeof COMPARE_RANGES)[number]['value'] {
  const hours = Math.max(0, (nowMs - fromMs) / 3600_000)
  return (COMPARE_RANGES.find((r) => r.hours >= hours) ?? COMPARE_RANGES[COMPARE_RANGES.length - 1]).value
}

/** The comparison page URL for a selection made on the trace list. */
export function compareUrl(opts: { projectId: number; selection: FilterExpr; fromMs: number; nowMs?: number }): string {
  const params = new URLSearchParams()
  if (opts.projectId) params.set('project', String(opts.projectId))
  params.set('range', rangeCovering(opts.fromMs, opts.nowMs))
  const selection = serializeFilter(opts.selection)
  if (selection) params.set('selection', selection)
  return `/compare?${params.toString()}`
}
