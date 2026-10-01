export type DashboardFilter = {
  from: string
  to: string
  projectId?: number
  service?: string
  name?: string
  status?: string
}

export type DashboardCountPoint = {
  time: string
  group: string
  count: number
}

export type DashboardCounts = {
  intervalSeconds: number
  points: DashboardCountPoint[]
}

/** Durations are microseconds. */
export type DashboardPercentilePoint = {
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
}
