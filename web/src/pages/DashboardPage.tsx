import { useCallback, useMemo, useState, type ReactElement } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import type { DashboardFilter } from '../api/dashboardTypes'
import { AutoRefresh } from '../components/AutoRefresh'
import { DashboardCard } from '../components/dashboard/DashboardCard'
import { DashboardFilters, type DashboardFilterValues } from '../components/dashboard/DashboardFilters'
import { HeatmapChart } from '../components/dashboard/HeatmapChart'
import { SeriesChart } from '../components/dashboard/SeriesChart'
import { TimeRangePicker } from '../components/dashboard/TimeRangePicker'
import { useDashboardQuery } from '../components/dashboard/useDashboardQuery'
import { pivotSeries } from '../utils/dashboardData'
import { DEFAULT_DASHBOARD_RANGE, findDashboardRange, getDashboardWindow } from '../utils/dashboardWindow'
import { formatCount, formatDuration } from '../utils/format'

const PERCENTILES = [
  { key: 'p99Us', label: 'P99(duration)' },
  { key: 'p95Us', label: 'P95(duration)' },
  { key: 'p90Us', label: 'P90(duration)' },
] as const

/** Default dashboard: counts, status codes, duration distribution and percentiles for the chosen window. */
export function DashboardPage(): ReactElement {
  const [params, setParams] = useSearchParams()
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [refreshKey, setRefreshKey] = useState(0)

  const range = findDashboardRange(params.get('range') ?? DEFAULT_DASHBOARD_RANGE).value
  const offset = Math.max(0, Number(params.get('offset')) || 0)
  const filters: DashboardFilterValues = {
    projectId: Number(params.get('project')) || 0,
    service: params.get('service') ?? '',
    name: params.get('name') ?? '',
    status: params.get('status') ?? '',
  }

  // Filters and range live in the URL so a view can be linked and survives reload.
  const update = useCallback(
    (changes: Record<string, string | number | undefined>) => {
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          for (const [k, v] of Object.entries(changes)) {
            if (v === undefined || v === '' || v === 0) next.delete(k)
            else next.set(k, String(v))
          }
          return next
        },
        { replace: true },
      )
    },
    [setParams],
  )

  // A refresh recomputes the window so "now" advances.
  // eslint-disable-next-line react-hooks/exhaustive-deps -- refreshKey is the trigger
  const win = useMemo(() => getDashboardWindow(range, offset), [range, offset, refreshKey])
  const { from, to, fromMs, toMs } = win
  const { projectId, service, name, status } = filters
  const filter: DashboardFilter = useMemo(
    () => ({ from, to, projectId, service, name, status }),
    [from, to, projectId, service, name, status],
  )

  const traceCountsByService = useDashboardQuery(filter, (f) => api.getDashboardCounts(f, 'service', true), refreshKey)
  const traceCountsByStatus = useDashboardQuery(filter, (f) => api.getDashboardCounts(f, 'http_status', true), refreshKey)
  const traceHeatmap = useDashboardQuery(filter, (f) => api.getDashboardHeatmap(f, true), refreshKey)
  const spanHeatmap = useDashboardQuery(filter, (f) => api.getDashboardHeatmap(f, false), refreshKey)
  const byService = useDashboardQuery(filter, (f) => api.getDashboardPercentiles(f, 'service'), refreshKey)
  const byName = useDashboardQuery(filter, (f) => api.getDashboardPercentiles(f, 'name'), refreshKey)

  const countsByService = useMemo(() => pivotSeries(traceCountsByService.data?.points ?? [], (p) => p.count), [traceCountsByService.data])
  const countsByStatus = useMemo(() => pivotSeries(traceCountsByStatus.data?.points ?? [], (p) => p.count), [traceCountsByStatus.data])

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: '0.75rem', marginBottom: '1rem' }}>
        <DashboardFilters
          values={filters}
          onChange={(v) => update({ project: v.projectId, service: v.service, name: v.name, status: v.status })}
        />
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
          <AutoRefresh value={refreshInterval} onChange={setRefreshInterval} onRefresh={() => setRefreshKey((k) => k + 1)} />
          <TimeRangePicker
            range={range}
            offset={offset}
            onRangeChange={(r) => update({ range: r === DEFAULT_DASHBOARD_RANGE ? undefined : r, offset: undefined })}
            onOffsetChange={(o) => update({ offset: o })}
          />
        </div>
      </div>

      <div className="charts-grid" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(360px, 1fr))', gap: '1rem', alignItems: 'start' }}>
        <DashboardCard
          title="Trace Counts by Service"
          info="Root spans per time bucket, one line per service. The ten busiest services get their own line; the rest are grouped as other."
          {...cardState(traceCountsByService, countsByService.rows.length === 0)}
        >
          <SeriesChart series={countsByService} fromMs={fromMs} toMs={toMs} label="COUNT" formatValue={formatCount} />
        </DashboardCard>

        <DashboardCard
          title="Trace Counts by HTTP Status Code"
          info="Root spans per time bucket by HTTP status code. Spans without a status code attribute are left out."
          {...cardState(traceCountsByStatus, countsByStatus.rows.length === 0)}
        >
          <SeriesChart series={countsByStatus} fromMs={fromMs} toMs={toMs} label="COUNT" formatValue={formatCount} byStatus />
        </DashboardCard>

        <DashboardCard
          title="Trace Duration Heatmap"
          info="Root span durations over time. Darker cells hold more traces; the scale is logarithmic."
          {...cardState(traceHeatmap, (traceHeatmap.data?.cells.length ?? 0) === 0)}
        >
          {traceHeatmap.data && <HeatmapChart heatmap={traceHeatmap.data} fromMs={fromMs} toMs={toMs} label="HEATMAP(duration)" />}
        </DashboardCard>

        <DashboardCard
          title="Duration Heatmap"
          info="Duration of every span over time. Darker cells hold more spans; the scale is logarithmic."
          {...cardState(spanHeatmap, (spanHeatmap.data?.cells.length ?? 0) === 0)}
        >
          {spanHeatmap.data && <HeatmapChart heatmap={spanHeatmap.data} fromMs={fromMs} toMs={toMs} label="HEATMAP(duration)" />}
        </DashboardCard>

        <PercentileCard
          title="Duration by Service"
          info="Exact duration percentiles per time bucket for the ten busiest services."
          state={byService}
          fromMs={fromMs}
          toMs={toMs}
        />

        <PercentileCard
          title="Duration by Name"
          info="Exact duration percentiles per time bucket for the ten busiest span names."
          state={byName}
          fromMs={fromMs}
          toMs={toMs}
        />
      </div>
    </div>
  )
}

function cardState(q: { loading: boolean; error: string | null }, empty: boolean) {
  return { loading: q.loading, error: q.error, empty }
}

type PercentileCardProps = {
  title: string
  info: string
  state: ReturnType<typeof useDashboardQuery<import('../api/dashboardTypes').DashboardPercentiles>>
  fromMs: number
  toMs: number
}

function PercentileCard({ title, info, state, fromMs, toMs }: PercentileCardProps): ReactElement {
  const points = state.data?.points
  const charts = useMemo(
    () => PERCENTILES.map((p) => ({ ...p, series: pivotSeries(points ?? [], (pt) => pt[p.key]) })),
    [points],
  )
  return (
    <DashboardCard title={title} info={info} {...cardState(state, (points?.length ?? 0) === 0)}>
      {charts.map((c) => (
        <SeriesChart key={c.key} series={c.series} fromMs={fromMs} toMs={toMs} label={c.label} formatValue={formatDuration} height={160} />
      ))}
    </DashboardCard>
  )
}
