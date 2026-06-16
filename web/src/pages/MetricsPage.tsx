import { useState, useEffect, useCallback, useMemo, useRef, type ReactElement } from 'react'
import {
  LineChart,
  Line,
  CartesianGrid,
  XAxis,
  YAxis,
  Tooltip,
  Legend,
  ResponsiveContainer,
} from 'recharts'
import { api } from '../api/client'
import type { MetricCatalogGroup, MetricInsight, MetricSeries, MetricSeriesResponse } from '../api/types'
import { TimeRangeSelector } from '../components/TimeRangeSelector'
import { AutoRefresh } from '../components/AutoRefresh'
import { getTimeRange } from '../utils/timeRange'
import { useTimeRange } from '../contexts/useTimeRange'

const PALETTE = ['#3b82f6', '#22c55e', '#eab308', '#ef4444', '#a855f7', '#06b6d4', '#f97316', '#ec4899']

export function MetricsPage(): ReactElement {
  const { range, setRange } = useTimeRange()
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [catalog, setCatalog] = useState<MetricCatalogGroup[]>([])
  const [insights, setInsights] = useState<MetricInsight[]>([])
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  const [nameFilter, setNameFilter] = useState('')
  const [selectedName, setSelectedName] = useState<string | null>(null)
  const [series, setSeries] = useState<MetricSeriesResponse | null>(null)
  const [groupBy, setGroupBy] = useState<string[]>([])
  const [loadingNames, setLoadingNames] = useState(true)
  const [loadingSeries, setLoadingSeries] = useState(false)
  const namesIdRef = useRef(0)
  const seriesIdRef = useRef(0)

  const fetchNames = useCallback(async () => {
    const id = ++namesIdRef.current
    const { from, to } = getTimeRange(range)
    try {
      const [cat, ins] = await Promise.all([api.getMetricCatalog(from, to), api.getMetricInsights(from, to)])
      if (id === namesIdRef.current) {
        setCatalog(cat?.groups ?? [])
        setInsights(ins?.insights ?? [])
      }
    } catch {
      // handled by client
    } finally {
      if (id === namesIdRef.current) setLoadingNames(false)
    }
  }, [range])

  const fetchSeries = useCallback(
    async (name: string, gb: string[]) => {
      const id = ++seriesIdRef.current
      const { from, to } = getTimeRange(range)
      try {
        const resp = await api.getMetricSeries(name, from, to, undefined, undefined, 0, gb)
        if (id === seriesIdRef.current) setSeries(resp ?? null)
      } catch {
        // handled by client
      } finally {
        if (id === seriesIdRef.current) setLoadingSeries(false)
      }
    },
    [range],
  )

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- reset loading when range changes
    setLoadingNames(true)
  }, [range])

  // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
  useEffect(() => { void fetchNames() }, [fetchNames])

  // Reset group-by when switching metric so keys from a previous metric don't leak.
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- reset on selection change
    setGroupBy([])
  }, [selectedName])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- reset series on selection change
    setSeries(null)
    if (selectedName) {
      setLoadingSeries(true)
      void fetchSeries(selectedName, groupBy)
    }
  }, [selectedName, groupBy, fetchSeries])

  // Filter metrics within each group by the search box, dropping empty groups.
  const q = nameFilter.toLowerCase()
  const filteredGroups = catalog
    .map((g) => ({ ...g, metrics: q ? g.metrics.filter((m) => m.name.toLowerCase().includes(q)) : g.metrics }))
    .filter((g) => g.metrics.length > 0)

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginBottom: '1.5rem', flexWrap: 'wrap' }}>
        <h1 style={{ margin: 0, fontSize: '1.25rem', fontWeight: 600 }}>Metrics</h1>
        <div style={{ flex: 1 }} />
        <AutoRefresh value={refreshInterval} onChange={setRefreshInterval} onRefresh={fetchNames} />
        <TimeRangeSelector value={range} onChange={setRange} />
      </div>

      {insights.length > 0 && <InsightsPanel insights={insights} onSelect={setSelectedName} />}

      <div style={{ display: 'flex', gap: '1rem', alignItems: 'flex-start' }}>
        {/* Left panel: metric names */}
        <div
          style={{
            width: 260,
            flexShrink: 0,
            background: 'var(--surface)',
            border: '1px solid var(--border)',
            borderRadius: '0.5rem',
            overflow: 'hidden',
          }}
        >
          <div style={{ padding: '0.75rem', borderBottom: '1px solid var(--border)' }}>
            <input
              type="text"
              placeholder="Filter metrics…"
              value={nameFilter}
              onChange={(e) => setNameFilter(e.target.value)}
              style={{
                width: '100%',
                background: 'var(--surface-hover)',
                border: '1px solid var(--border)',
                borderRadius: '0.375rem',
                color: 'var(--text)',
                fontSize: '0.8125rem',
                padding: '0.375rem 0.5rem',
                outline: 'none',
                boxSizing: 'border-box',
              }}
            />
          </div>
          <div style={{ maxHeight: 480, overflowY: 'auto' }}>
            {loadingNames ? (
              <div style={{ padding: '1rem', color: 'var(--text-muted)', fontSize: '0.8125rem' }}>Loading…</div>
            ) : filteredGroups.length === 0 ? (
              <div style={{ padding: '1rem', color: 'var(--text-muted)', fontSize: '0.8125rem' }}>No metrics found</div>
            ) : (
              filteredGroups.map((g) => {
                // While filtering, expand all so matches are visible.
                const isCollapsed = !q && collapsed[g.name]
                return (
                  <div key={g.name}>
                    <button
                      onClick={() => setCollapsed((c) => ({ ...c, [g.name]: !c[g.name] }))}
                      style={{
                        display: 'flex',
                        alignItems: 'center',
                        gap: '0.4rem',
                        width: '100%',
                        textAlign: 'left',
                        padding: '0.4rem 0.75rem',
                        fontSize: '0.6875rem',
                        fontWeight: 600,
                        textTransform: 'uppercase',
                        letterSpacing: '0.04em',
                        background: 'var(--surface-hover)',
                        color: 'var(--text-muted)',
                        border: 'none',
                        borderTop: '1px solid var(--border)',
                        cursor: 'pointer',
                      }}
                    >
                      <span style={{ fontSize: '0.625rem' }}>{isCollapsed ? '▸' : '▾'}</span>
                      {g.name}
                      <span style={{ marginLeft: 'auto', fontWeight: 400 }}>{g.metrics.length}</span>
                    </button>
                    {!isCollapsed &&
                      g.metrics.map((m) => (
                        <button
                          key={m.name}
                          onClick={() => setSelectedName(m.name)}
                          title={`${m.type}${m.unit ? ` · ${m.unit}` : ''} · ${m.series} series`}
                          style={{
                            display: 'flex',
                            alignItems: 'center',
                            gap: '0.4rem',
                            width: '100%',
                            textAlign: 'left',
                            padding: '0.5rem 0.75rem',
                            fontSize: '0.8125rem',
                            fontFamily: 'monospace',
                            background: selectedName === m.name ? 'var(--surface-hover)' : 'transparent',
                            color: selectedName === m.name ? 'var(--text)' : 'var(--text-muted)',
                            border: 'none',
                            borderLeft: selectedName === m.name ? '2px solid var(--accent)' : '2px solid transparent',
                            cursor: 'pointer',
                          }}
                        >
                          <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{m.name}</span>
                          {m.series > 1 && (
                            <span style={{ marginLeft: 'auto', fontSize: '0.6875rem', color: 'var(--text-muted)', flexShrink: 0 }}>
                              {m.series}×
                            </span>
                          )}
                        </button>
                      ))}
                  </div>
                )
              })
            )}
          </div>
        </div>

        {/* Right panel: time series chart */}
        <div style={{ flex: 1, minWidth: 0 }}>
          {!selectedName ? (
            <Placeholder>Select a metric to view its time series</Placeholder>
          ) : loadingSeries ? (
            <Placeholder>Loading…</Placeholder>
          ) : series ? (
            <MetricChart
              series={series}
              groupBy={groupBy}
              onToggleGroupBy={(key) =>
                setGroupBy((cur) => (cur.includes(key) ? cur.filter((k) => k !== key) : [...cur, key]))
              }
            />
          ) : null}
        </div>
      </div>
    </div>
  )
}

function Placeholder({ children }: { children: React.ReactNode }): ReactElement {
  return (
    <div
      style={{
        background: 'var(--surface)',
        border: '1px solid var(--border)',
        borderRadius: '0.5rem',
        padding: '3rem',
        textAlign: 'center',
        color: 'var(--text-muted)',
        fontSize: '0.875rem',
      }}
    >
      {children}
    </div>
  )
}

const INSIGHT_STYLE: Record<MetricInsight['kind'], { label: string; color: string }> = {
  spike: { label: 'Spike', color: '#ef4444' },
  drop: { label: 'Drop', color: '#3b82f6' },
  regression: { label: 'p95 ↑', color: '#eab308' },
  new_series: { label: 'New', color: '#22c55e' },
}

function labelSuffix(labels: Record<string, string>): string {
  const entries = Object.entries(labels)
  if (entries.length === 0) return ''
  return ` {${entries.map(([k, v]) => `${k}=${v}`).join(', ')}}`
}

function InsightsPanel({
  insights,
  onSelect,
}: {
  insights: MetricInsight[]
  onSelect: (name: string) => void
}): ReactElement {
  return (
    <div style={{ marginBottom: '1.25rem' }}>
      <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.04em', marginBottom: '0.5rem' }}>
        Notable changes
      </div>
      <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
        {insights.map((ins, i) => {
          const s = INSIGHT_STYLE[ins.kind]
          const pct = ins.kind === 'new_series' ? '' : `${ins.changePct > 0 ? '+' : ''}${Math.round(ins.changePct * 100)}%`
          return (
            <button
              key={`${ins.metric}-${i}`}
              onClick={() => onSelect(ins.metric)}
              title={ins.kind === 'new_series' ? 'Newly appeared' : `${ins.baseline.toLocaleString()} → ${ins.recent.toLocaleString()}`}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: '0.5rem',
                padding: '0.4rem 0.6rem',
                background: 'var(--surface)',
                border: '1px solid var(--border)',
                borderLeft: `3px solid ${s.color}`,
                borderRadius: '0.375rem',
                cursor: 'pointer',
                textAlign: 'left',
              }}
            >
              <span style={{ fontSize: '0.625rem', fontWeight: 700, color: s.color, textTransform: 'uppercase' }}>{s.label}</span>
              <span style={{ fontSize: '0.8125rem', fontFamily: 'monospace', color: 'var(--text)' }}>
                {ins.metric}
                <span style={{ color: 'var(--text-muted)' }}>{labelSuffix(ins.labels)}</span>
              </span>
              {pct && <span style={{ fontSize: '0.75rem', fontWeight: 600, color: s.color }}>{pct}</span>}
            </button>
          )
        })}
      </div>
    </div>
  )
}

const RENDER_HINT: Record<string, string> = {
  line: 'Current value over time',
  rate: 'Per-second rate (counter increase ÷ elapsed time)',
  percentile: 'p50 / p95 / p99 reconstructed from the distribution',
}

function labelText(labels: Record<string, string>): string {
  const entries = Object.entries(labels)
  if (entries.length === 0) return 'value'
  return entries.map(([k, v]) => `${k}=${v}`).join(', ')
}

function fmtTime(nanos: number): string {
  const d = new Date(nanos / 1_000_000)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

type ChartRow = { t: number; time: string; [key: string]: number | string }
type LineDef = { key: string; name: string; color: string }

// buildChart flattens the per-series points into a single recharts dataset keyed
// by timestamp, and decides which lines to draw based on the render kind.
function buildChart(resp: MetricSeriesResponse): { rows: ChartRow[]; lines: LineDef[] } {
  const { render, series } = resp

  const tset = new Set<number>()
  series.forEach((s) => s.points.forEach((p) => tset.add(p.t)))
  const times = [...tset].sort((a, b) => a - b)
  const rowByT = new Map<number, ChartRow>()
  times.forEach((t) => rowByT.set(t, { t, time: fmtTime(t) }))

  const lines: LineDef[] = []

  if (render === 'percentile' && series.length === 1) {
    // Single distribution: show the three percentile bands.
    lines.push(
      { key: 'p99', name: 'p99', color: '#ef4444' },
      { key: 'p95', name: 'p95', color: '#eab308' },
      { key: 'p50', name: 'p50', color: '#3b82f6' },
    )
    series[0].points.forEach((p) => {
      const row = rowByT.get(p.t)!
      if (p.p50 != null) row.p50 = p.p50
      if (p.p95 != null) row.p95 = p.p95
      if (p.p99 != null) row.p99 = p.p99
    })
  } else if (render === 'percentile') {
    // Multiple distributions: one p95 line per series to keep it legible.
    series.forEach((s, i) => {
      const key = `s${i}`
      lines.push({ key, name: `${labelText(s.labels)} · p95`, color: PALETTE[i % PALETTE.length] })
      s.points.forEach((p) => {
        if (p.p95 != null) rowByT.get(p.t)![key] = p.p95
      })
    })
  } else {
    // Gauge value or counter rate: one line per series.
    series.forEach((s, i) => {
      const key = `s${i}`
      lines.push({ key, name: labelText(s.labels), color: PALETTE[i % PALETTE.length] })
      s.points.forEach((p) => {
        rowByT.get(p.t)![key] = p.value
      })
    })
  }

  return { rows: times.map((t) => rowByT.get(t)!), lines }
}

// availableKeys returns the union of attribute keys across the series labels,
// so the user can pick which one to group by.
function availableKeys(series: MetricSeries[]): string[] {
  const keys = new Set<string>()
  series.forEach((s) => Object.keys(s.labels).forEach((k) => keys.add(k)))
  return [...keys].sort()
}

function MetricChart({
  series: resp,
  groupBy,
  onToggleGroupBy,
}: {
  series: MetricSeriesResponse
  groupBy: string[]
  onToggleGroupBy: (key: string) => void
}): ReactElement {
  const { name, type, unit, render, series } = resp
  const { rows, lines } = useMemo(() => buildChart(resp), [resp])
  const keys = useMemo(() => availableKeys(series), [series])

  const totalPoints = series.reduce((n, s) => n + s.points.length, 0)

  return (
    <div
      style={{
        background: 'var(--surface)',
        border: '1px solid var(--border)',
        borderRadius: '0.5rem',
        padding: '1rem',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'baseline', gap: '0.5rem', marginBottom: '0.25rem', flexWrap: 'wrap' }}>
        <span style={{ fontFamily: 'monospace', fontSize: '0.875rem', fontWeight: 600 }}>{name}</span>
        <span style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>
          {type}
          {unit ? ` · ${unit}` : ''}
        </span>
        <span style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginLeft: 'auto' }}>
          {series.length} series · {totalPoints} points
        </span>
      </div>
      <p style={{ margin: '0 0 0.75rem', fontSize: '0.75rem', color: 'var(--text-muted)' }}>{RENDER_HINT[render]}</p>

      {keys.length > 0 && (
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', marginBottom: '0.75rem', flexWrap: 'wrap' }}>
          <span style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>Group by:</span>
          {keys.map((k) => {
            const active = groupBy.includes(k)
            return (
              <button
                key={k}
                onClick={() => onToggleGroupBy(k)}
                style={{
                  fontSize: '0.75rem',
                  fontFamily: 'monospace',
                  padding: '0.2rem 0.5rem',
                  borderRadius: '0.375rem',
                  border: `1px solid ${active ? 'var(--accent)' : 'var(--border)'}`,
                  background: active ? 'var(--accent)' : 'transparent',
                  color: active ? '#fff' : 'var(--text-muted)',
                  cursor: 'pointer',
                }}
              >
                {k}
              </button>
            )
          })}
        </div>
      )}

      {rows.length === 0 ? (
        <Placeholder>No data points in selected range</Placeholder>
      ) : (
        <ResponsiveContainer width="100%" height={260}>
          <LineChart data={rows} margin={{ top: 4, right: 8, bottom: 0, left: -8 }}>
            <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
            <XAxis dataKey="time" tick={{ fontSize: 11, fill: 'var(--text-muted)' }} />
            <YAxis tick={{ fontSize: 11, fill: 'var(--text-muted)' }} width={48} />
            <Tooltip
              contentStyle={{
                background: 'var(--surface)',
                border: '1px solid var(--border)',
                borderRadius: 8,
                color: 'var(--text)',
              }}
              formatter={(value: number | string) => [`${Number(value).toLocaleString()}${unit ? ` ${unit}` : ''}`]}
            />
            <Legend verticalAlign="top" align="right" iconType="line" wrapperStyle={{ fontSize: 11, paddingBottom: 4 }} />
            {lines.map((l) => (
              <Line
                key={l.key}
                type="monotone"
                dataKey={l.key}
                name={l.name}
                stroke={l.color}
                dot={false}
                strokeWidth={2}
                connectNulls
              />
            ))}
          </LineChart>
        </ResponsiveContainer>
      )}
    </div>
  )
}
