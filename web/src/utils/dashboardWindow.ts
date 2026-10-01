export type DashboardRange = {
  value: string
  label: string
  hours: number
}

/** The dashboard reads raw spans, so the widest range is 48h. */
export const DASHBOARD_RANGES: DashboardRange[] = [
  { value: '1h', label: 'Last 1 hour', hours: 1 },
  { value: '4h', label: 'Last 4 hours', hours: 4 },
  { value: '24h', label: 'Last 1 day', hours: 24 },
  { value: '48h', label: 'Last 2 days', hours: 48 },
]

export const DEFAULT_DASHBOARD_RANGE = '24h'

export type DashboardWindow = {
  from: string
  to: string
  fromMs: number
  toMs: number
}

export function findDashboardRange(value: string): DashboardRange {
  return DASHBOARD_RANGES.find((r) => r.value === value) ?? DASHBOARD_RANGES.find((r) => r.value === DEFAULT_DASHBOARD_RANGE)!
}

/**
 * The window `offset` widths back from now. Offset 0 ends at `now`; offset 1 is
 * the window immediately before it, and so on. Negative offsets clamp to 0
 * because the dashboard has no data from the future.
 */
export function getDashboardWindow(rangeValue: string, offset: number, now: Date = new Date()): DashboardWindow {
  const widthMs = findDashboardRange(rangeValue).hours * 3600_000
  const steps = Math.max(0, Math.floor(offset))
  const toMs = now.getTime() - steps * widthMs
  const fromMs = toMs - widthMs
  return {
    from: new Date(fromMs).toISOString(),
    to: new Date(toMs).toISOString(),
    fromMs,
    toMs,
  }
}
