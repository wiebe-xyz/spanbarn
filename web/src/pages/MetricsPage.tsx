import { useState, useEffect, useCallback, useRef, type ReactElement } from 'react'
import { api } from '../api/client'
import type { MetricPoint, MetricSeriesResponse } from '../api/types'
import { TimeRangeSelector } from '../components/TimeRangeSelector'
import { AutoRefresh } from '../components/AutoRefresh'
import { getTimeRange } from '../utils/timeRange'
import { useTimeRange } from '../contexts/useTimeRange'

export function MetricsPage(): ReactElement {
  const { range, setRange } = useTimeRange()
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [names, setNames] = useState<string[]>([])
  const [nameFilter, setNameFilter] = useState('')
  const [selectedName, setSelectedName] = useState<string | null>(null)
  const [series, setSeries] = useState<MetricSeriesResponse | null>(null)
  const [loadingNames, setLoadingNames] = useState(true)
  const [loadingSeries, setLoadingSeries] = useState(false)
  const namesIdRef = useRef(0)
  const seriesIdRef = useRef(0)

  const fetchNames = useCallback(async () => {
    const id = ++namesIdRef.current
    const { from, to } = getTimeRange(range)
    try {
      const resp = await api.getMetricNames(from, to)
      if (id === namesIdRef.current) {
        setNames(resp?.names ?? [])
      }
    } catch {
      // handled by client
    } finally {
      if (id === namesIdRef.current) setLoadingNames(false)
    }
  }, [range])

  const fetchSeries = useCallback(async (name: string) => {
    const id = ++seriesIdRef.current
    const { from, to } = getTimeRange(range)
    try {
      const resp = await api.getMetricSeries(name, from, to)
      if (id === seriesIdRef.current) setSeries(resp ?? null)
    } catch {
      // handled by client
    } finally {
      if (id === seriesIdRef.current) setLoadingSeries(false)
    }
  }, [range])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- reset loading when range changes
    setLoadingNames(true)
  }, [range])

  // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
  useEffect(() => { void fetchNames() }, [fetchNames])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- reset series on selection change
    setSeries(null)
    if (selectedName) {
      setLoadingSeries(true)
      void fetchSeries(selectedName)
    }
  }, [selectedName, fetchSeries])

  const filteredNames = nameFilter
    ? names.filter((n) => n.toLowerCase().includes(nameFilter.toLowerCase()))
    : names

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginBottom: '1.5rem', flexWrap: 'wrap' }}>
        <h1 style={{ margin: 0, fontSize: '1.25rem', fontWeight: 600 }}>Metrics</h1>
        <div style={{ flex: 1 }} />
        <AutoRefresh value={refreshInterval} onChange={setRefreshInterval} onRefresh={fetchNames} />
        <TimeRangeSelector value={range} onChange={setRange} />
      </div>

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
            ) : filteredNames.length === 0 ? (
              <div style={{ padding: '1rem', color: 'var(--text-muted)', fontSize: '0.8125rem' }}>No metrics found</div>
            ) : (
              filteredNames.map((name) => (
                <button
                  key={name}
                  onClick={() => setSelectedName(name)}
                  style={{
                    display: 'block',
                    width: '100%',
                    textAlign: 'left',
                    padding: '0.5rem 0.75rem',
                    fontSize: '0.8125rem',
                    fontFamily: 'monospace',
                    background: selectedName === name ? 'var(--surface-hover)' : 'transparent',
                    color: selectedName === name ? 'var(--text)' : 'var(--text-muted)',
                    border: 'none',
                    borderLeft: selectedName === name ? '2px solid var(--accent)' : '2px solid transparent',
                    cursor: 'pointer',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                  }}
                >
                  {name}
                </button>
              ))
            )}
          </div>
        </div>

        {/* Right panel: time series chart */}
        <div style={{ flex: 1, minWidth: 0 }}>
          {!selectedName ? (
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
              Select a metric to view its time series
            </div>
          ) : loadingSeries ? (
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
              Loading…
            </div>
          ) : series ? (
            <MetricChart series={series} />
          ) : null}
        </div>
      </div>
    </div>
  )
}

function MetricChart({ series }: { series: MetricSeriesResponse }): ReactElement {
  const { name, type, unit, points } = series

  if (points.length === 0) {
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
        No data points in selected range
      </div>
    )
  }

  const useCount = type === 'histogram' || type === 'exp_histogram' || type === 'summary'
  const values = points.map((p) => (useCount ? p.count : p.value))
  const minVal = Math.min(...values)
  const maxVal = Math.max(...values)
  const minT = points[0].t
  const maxT = points[points.length - 1].t

  const W = 600
  const H = 160
  const pad = { top: 8, right: 8, bottom: 24, left: 48 }
  const chartW = W - pad.left - pad.right
  const chartH = H - pad.top - pad.bottom

  const tRange = maxT - minT || 1
  const vRange = maxVal - minVal || 1

  const toX = (t: number) => pad.left + ((t - minT) / tRange) * chartW
  const toY = (v: number) => pad.top + chartH - ((v - minVal) / vRange) * chartH

  const polylinePoints = points.map((p) => `${toX(p.t)},${toY(useCount ? p.count : p.value)}`).join(' ')

  // Y-axis ticks: 3 evenly spaced
  const yTicks = [minVal, minVal + vRange / 2, maxVal]
  // X-axis ticks: first and last timestamp
  const xTicks = [minT, maxT]

  const formatVal = (v: number) => {
    if (Math.abs(v) >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`
    if (Math.abs(v) >= 1_000) return `${(v / 1_000).toFixed(1)}k`
    return v % 1 === 0 ? String(v) : v.toFixed(2)
  }

  const formatTime = (nanos: number) => {
    const ms = nanos / 1_000_000
    const d = new Date(ms)
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }

  return (
    <div
      style={{
        background: 'var(--surface)',
        border: '1px solid var(--border)',
        borderRadius: '0.5rem',
        padding: '1rem',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'baseline', gap: '0.5rem', marginBottom: '0.75rem' }}>
        <span style={{ fontFamily: 'monospace', fontSize: '0.875rem', fontWeight: 600 }}>{name}</span>
        <span style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>{type}{unit ? ` · ${unit}` : ''}</span>
        <span style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginLeft: 'auto' }}>{points.length} points</span>
      </div>
      <svg
        viewBox={`0 0 ${W} ${H}`}
        style={{ width: '100%', height: 'auto', display: 'block' }}
        aria-label={`Time series chart for ${name}`}
      >
        {/* Y-axis ticks */}
        {yTicks.map((v, i) => (
          <g key={i}>
            <line
              x1={pad.left}
              y1={toY(v)}
              x2={pad.left + chartW}
              y2={toY(v)}
              stroke="var(--border)"
              strokeWidth={0.5}
              strokeDasharray="3 3"
            />
            <text
              x={pad.left - 4}
              y={toY(v) + 4}
              textAnchor="end"
              fontSize={9}
              fill="var(--text-muted)"
            >
              {formatVal(v)}
            </text>
          </g>
        ))}

        {/* X-axis ticks */}
        {xTicks.map((t, i) => (
          <text
            key={i}
            x={toX(t)}
            y={H - 4}
            textAnchor={i === 0 ? 'start' : 'end'}
            fontSize={9}
            fill="var(--text-muted)"
          >
            {formatTime(t)}
          </text>
        ))}

        {/* Series line */}
        <polyline
          points={polylinePoints}
          fill="none"
          stroke="var(--accent)"
          strokeWidth={1.5}
          strokeLinejoin="round"
          strokeLinecap="round"
        />

        {/* Dots for sparse data */}
        {points.length <= 30 &&
          points.map((p, i) => (
            <circle
              key={i}
              cx={toX(p.t)}
              cy={toY(useCount ? p.count : p.value)}
              r={2.5}
              fill="var(--accent)"
            />
          ))}
      </svg>
      {useCount && (
        <p style={{ margin: '0.25rem 0 0', fontSize: '0.75rem', color: 'var(--text-muted)' }}>
          Showing observation count over time (type: {type})
        </p>
      )}
      <LatestValues points={points} useCount={useCount} />
    </div>
  )
}

function LatestValues({ points, useCount }: { points: MetricPoint[]; useCount: boolean }): ReactElement {
  const latest = points[points.length - 1]
  const displayValue = useCount ? latest.count : latest.value
  const attrs = latest.attributes as Record<string, string>
  const attrEntries = Object.entries(attrs ?? {})

  return (
    <div style={{ marginTop: '0.75rem', display: 'flex', gap: '1.5rem', flexWrap: 'wrap' }}>
      <div>
        <span style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>Latest value</span>
        <div style={{ fontSize: '1.125rem', fontWeight: 600, fontVariantNumeric: 'tabular-nums' }}>
          {typeof displayValue === 'number' && displayValue % 1 !== 0
            ? displayValue.toFixed(3)
            : String(displayValue)}
        </div>
      </div>
      {attrEntries.slice(0, 4).map(([k, v]) => (
        <div key={k}>
          <span style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>{k}</span>
          <div style={{ fontSize: '0.875rem', fontFamily: 'monospace' }}>{v}</div>
        </div>
      ))}
    </div>
  )
}
