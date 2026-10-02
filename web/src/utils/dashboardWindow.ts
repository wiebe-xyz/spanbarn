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

/** Narrowest and widest custom window; they match what the server accepts. */
export const MIN_ZOOM_MS = 60_000
export const MAX_ZOOM_MS = 48 * 3600_000

function windowOf(fromMs: number, toMs: number): DashboardWindow {
  return { from: new Date(fromMs).toISOString(), to: new Date(toMs).toISOString(), fromMs, toMs }
}

/**
 * A window between two instants, kept inside what the server serves: at least a
 * minute and at most 48h wide, and never ending after `now`. A window that is
 * too narrow grows around its centre; one that ends in the future shifts back.
 */
export function customWindow(fromMs: number, toMs: number, now: Date = new Date()): DashboardWindow {
  const width = Math.min(MAX_ZOOM_MS, Math.max(MIN_ZOOM_MS, Math.round(toMs - fromMs)))
  const centre = (fromMs + toMs) / 2
  const end = Math.min(now.getTime(), Math.round(centre + width / 2))
  return windowOf(end - width, end)
}

/** Twice the width around the same centre. */
export function zoomOutWindow(fromMs: number, toMs: number, now: Date = new Date()): DashboardWindow {
  const centre = (fromMs + toMs) / 2
  const width = toMs - fromMs
  return customWindow(centre - width, centre + width, now)
}

/** Reads the `from` and `to` epoch-millisecond URL params; null unless both are valid. */
export function parseCustomWindow(from: string | null, to: string | null, now: Date = new Date()): DashboardWindow | null {
  const f = Number(from)
  const t = Number(to)
  if (!from || !to || !Number.isFinite(f) || !Number.isFinite(t) || t <= f) return null
  return customWindow(f, t, now)
}
