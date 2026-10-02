import { useCallback, useMemo, useState, type ReactElement } from 'react'
import { api } from '../api/client'
import type { DashboardFilter, DashboardPercentiles } from '../api/dashboardTypes'
import { AutoRefresh } from '../components/AutoRefresh'
import { ActiveFilters, type ChipKey } from '../components/dashboard/ActiveFilters'
import { DashboardCard } from '../components/dashboard/DashboardCard'
import { DashboardFilters } from '../components/dashboard/DashboardFilters'
import { GroupSelect } from '../components/dashboard/GroupSelect'
import { HeatmapChart, type HeatmapSelection } from '../components/dashboard/HeatmapChart'
import { SeriesChart } from '../components/dashboard/SeriesChart'
import { TimeRangePicker } from '../components/dashboard/TimeRangePicker'
import { useDashboardParams, COUNTS_GROUPS } from '../components/dashboard/useDashboardParams'
import { useDashboardQuery } from '../components/dashboard/useDashboardQuery'
import { pivotSeries } from '../utils/dashboardData'
import {
  DEFAULT_DASHBOARD_RANGE,
  customWindow,
  getDashboardWindow,
  parseCustomWindow,
  zoomOutWindow,
} from '../utils/dashboardWindow'
import { formatCount, formatDuration } from '../utils/format'

const PERCENTILES = [
  { key: 'p99Us', label: 'P99(duration)' },
  { key: 'p95Us', label: 'P95(duration)' },
  { key: 'p90Us', label: 'P90(duration)' },
] as const

/** A drag narrower than this share of the window counts as a click, not a zoom. */
const MIN_BRUSH_SHARE = 0.02

/** Default dashboard: counts, status codes, duration distribution and percentiles for the chosen window. */
export function DashboardPage(): ReactElement {
  const { state, update } = useDashboardParams()
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [refreshKey, setRefreshKey] = useState(0)
  const { range, offset, filters, minUs, maxUs, countsGroup } = state

  // A refresh recomputes a preset window so "now" advances; a zoomed window is fixed.
  const win = useMemo(
    () => parseCustomWindow(state.customFrom, state.customTo) ?? getDashboardWindow(range, offset),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- refreshKey is the trigger
    [range, offset, state.customFrom, state.customTo, refreshKey],
  )
  const zoomed = parseCustomWindow(state.customFrom, state.customTo) !== null
  const { from, to, fromMs, toMs } = win
  const { projectId, service, name, status } = filters
  const filter: DashboardFilter = useMemo(
    () => ({ from, to, projectId, service, name, status, minDurationUs: minUs, maxDurationUs: maxUs }),
    [from, to, projectId, service, name, status, minUs, maxUs],
  )

  const zoomTo = useCallback(
    (a: number, b: number, band?: { minUs: number; maxUs: number }) => {
      if (b - a < (toMs - fromMs) * MIN_BRUSH_SHARE) return
      const w = customWindow(a, b)
      const bandChanges = band && (band.minUs > 0 || band.maxUs > 0) ? { min_us: band.minUs, max_us: band.maxUs } : {}
      update({ from: w.fromMs, to: w.toMs, range: undefined, offset: undefined, ...bandChanges }, true)
    },
    [update, fromMs, toMs],
  )
  const zoomHeatmap = useCallback((s: HeatmapSelection) => zoomTo(s.fromMs, s.toMs, s), [zoomTo])
  const zoomOut = useCallback(() => {
    const w = zoomOutWindow(fromMs, toMs)
    update({ from: w.fromMs, to: w.toMs }, true)
  }, [update, fromMs, toMs])
  const clearChip = useCallback(
    (key: ChipKey) => update(key === 'band' ? { min_us: undefined, max_us: undefined } : { [key]: undefined }),
    [update],
  )
  const clearAll = useCallback(
    () => update({ service: undefined, name: undefined, status: undefined, min_us: undefined, max_us: undefined }),
    [update],
  )

  // useDashboardQuery refetches when the filter object changes, so a new group needs a new object.
  const countsFilter = useMemo(() => ({ ...filter }), [filter, countsGroup]) // eslint-disable-line react-hooks/exhaustive-deps
  const traceCounts = useDashboardQuery(countsFilter, (f) => api.getDashboardCounts(f, countsGroup, true), refreshKey)
  const traceCountsByStatus = useDashboardQuery(filter, (f) => api.getDashboardCounts(f, 'http_status', true), refreshKey)
  const traceHeatmap = useDashboardQuery(filter, (f) => api.getDashboardHeatmap(f, true), refreshKey)
  const spanHeatmap = useDashboardQuery(filter, (f) => api.getDashboardHeatmap(f, false), refreshKey)
  const byService = useDashboardQuery(filter, (f) => api.getDashboardPercentiles(f, 'service'), refreshKey)
  const byName = useDashboardQuery(filter, (f) => api.getDashboardPercentiles(f, 'name'), refreshKey)

  const counts = useMemo(() => pivotSeries(traceCounts.data?.points ?? [], (p) => p.count), [traceCounts.data])
  const countsByStatus = useMemo(() => pivotSeries(traceCountsByStatus.data?.points ?? [], (p) => p.count), [traceCountsByStatus.data])
  const countsGroupLabel = COUNTS_GROUPS.find((g) => g.value === countsGroup)?.label ?? 'Service'

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
            custom={zoomed}
            onRangeChange={(r) =>
              update({ range: r === DEFAULT_DASHBOARD_RANGE ? undefined : r, offset: undefined, from: undefined, to: undefined })
            }
            onOffsetChange={(o) => update({ offset: o })}
          />
        </div>
      </div>

      <ActiveFilters
        zoom={zoomed ? { fromMs, toMs } : null}
        minUs={minUs}
        maxUs={maxUs}
        service={service}
        name={name}
        status={status}
        onZoomOut={zoomOut}
        onResetZoom={() => update({ from: undefined, to: undefined, min_us: undefined, max_us: undefined }, true)}
        onClear={clearChip}
        onClearAll={clearAll}
      />

      <div className="charts-grid" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(360px, 1fr))', gap: '1rem', alignItems: 'start' }}>
        <DashboardCard
          title={`Trace Counts by ${countsGroupLabel}`}
          info="Root spans per time bucket, one line per group. The ten busiest groups get their own line; the rest are grouped as other. Drag across the chart to zoom; click a name below it to filter."
          {...cardState(traceCounts, counts.rows.length === 0)}
        >
          <GroupSelect value={countsGroup} options={COUNTS_GROUPS} onChange={(g) => update({ cg: g === 'service' ? undefined : g })} />
          <SeriesChart
            series={counts}
            fromMs={fromMs}
            toMs={toMs}
            label="COUNT"
            formatValue={formatCount}
            onBrush={zoomTo}
            onSelectGroup={(g) => update({ [countsGroup]: g })}
          />
        </DashboardCard>

        <DashboardCard
          title="Trace Counts by HTTP Status Code"
          info="Root spans per time bucket by HTTP status code. Spans without a status code attribute are left out."
          {...cardState(traceCountsByStatus, countsByStatus.rows.length === 0)}
        >
          <SeriesChart series={countsByStatus} fromMs={fromMs} toMs={toMs} label="COUNT" formatValue={formatCount} byStatus onBrush={zoomTo} />
        </DashboardCard>

        <DashboardCard
          title="Trace Duration Heatmap"
          info="Root span durations over time. Darker cells hold more traces; the scale is logarithmic. Drag a rectangle to zoom in time and keep only that duration band."
          {...cardState(traceHeatmap, (traceHeatmap.data?.cells.length ?? 0) === 0)}
        >
          {traceHeatmap.data && (
            <HeatmapChart heatmap={traceHeatmap.data} fromMs={fromMs} toMs={toMs} label="HEATMAP(duration)" onBrush={zoomHeatmap} />
          )}
        </DashboardCard>

        <DashboardCard
          title="Duration Heatmap"
          info="Duration of every span over time. Darker cells hold more spans; the scale is logarithmic. Drag a rectangle to zoom in time and keep only that duration band."
          {...cardState(spanHeatmap, (spanHeatmap.data?.cells.length ?? 0) === 0)}
        >
          {spanHeatmap.data && (
            <HeatmapChart heatmap={spanHeatmap.data} fromMs={fromMs} toMs={toMs} label="HEATMAP(duration)" onBrush={zoomHeatmap} />
          )}
        </DashboardCard>

        <PercentileCard
          title="Duration by Service"
          info="Exact duration percentiles per time bucket for the ten busiest services."
          state={byService}
          fromMs={fromMs}
          toMs={toMs}
          onBrush={zoomTo}
          onSelectGroup={(g) => update({ service: g })}
        />

        <PercentileCard
          title="Duration by Name"
          info="Exact duration percentiles per time bucket for the ten busiest span names."
          state={byName}
          fromMs={fromMs}
          toMs={toMs}
          onBrush={zoomTo}
          onSelectGroup={(g) => update({ name: g })}
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
  state: ReturnType<typeof useDashboardQuery<DashboardPercentiles>>
  fromMs: number
  toMs: number
  onBrush: (fromMs: number, toMs: number) => void
  onSelectGroup: (group: string) => void
}

function PercentileCard({ title, info, state, fromMs, toMs, onBrush, onSelectGroup }: PercentileCardProps): ReactElement {
  const points = state.data?.points
  const charts = useMemo(
    () => PERCENTILES.map((p) => ({ ...p, series: pivotSeries(points ?? [], (pt) => pt[p.key]) })),
    [points],
  )
  return (
    <DashboardCard title={title} info={info} {...cardState(state, (points?.length ?? 0) === 0)}>
      {charts.map((c) => (
        <SeriesChart
          key={c.key}
          series={c.series}
          fromMs={fromMs}
          toMs={toMs}
          label={c.label}
          formatValue={formatDuration}
          height={160}
          onBrush={onBrush}
          onSelectGroup={onSelectGroup}
        />
      ))}
    </DashboardCard>
  )
}
