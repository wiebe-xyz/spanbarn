export type DashboardFilter = {
  from: string
  to: string
  projectId?: number
  service?: string
  name?: string
  status?: string
  /** Span duration band in microseconds; 0 or unset means no bound. */
  minDurationUs?: number
  maxDurationUs?: number
}

/** The store a dashboard result came from: rollups count received spans, spans count stored ones. */
export type DataSource = 'rollups' | 'spans' | 'aggregates'

type DashboardCountPoint = {
  time: string
  group: string
  count: number
}

export type DashboardCounts = {
  intervalSeconds: number
  points: DashboardCountPoint[]
  source?: DataSource
}

/** Durations are microseconds. */
type DashboardPercentilePoint = {
  time: string
  group: string
  count: number
  p90Us: number
  p95Us: number
  p99Us: number
}

export type DashboardPercentiles = {
  intervalSeconds: number
  points: DashboardPercentilePoint[]
  source?: DataSource
}

/** One populated heatmap cell; the duration bucket covers [lowerUs, upperUs). */
export type DashboardHeatmapCell = {
  time: string
  bucket: number
  lowerUs: number
  upperUs: number
  count: number
}

export type DashboardHeatmap = {
  intervalSeconds: number
  cells: DashboardHeatmapCell[]
  source?: DataSource
}

/** Dimensions the counts card can group by. */
export type CountsGroup = 'service' | 'name' | 'status' | 'http_status'
export type PercentileGroup = 'service' | 'name' | 'status'
