import { useState, useEffect, useCallback, useMemo, type ReactElement } from 'react'
import { Search } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { WebVitalSummary, WebVitalTimeseriesBucket } from '../api/types'
import { TimeRangeSelector } from '../components/TimeRangeSelector'
import { getTimeRange } from '../utils/timeRange'
import { AutoRefresh } from '../components/AutoRefresh'
import { useTimeRange } from '../contexts/useTimeRange'
import { Sparkline } from '../components/Sparkline'

const METRIC_ORDER = ['LCP', 'FCP', 'TTFB', 'CLS', 'INP'] as const

const METRIC_LABELS: Record<string, string> = {
  LCP: 'Largest Contentful Paint',
  FCP: 'First Contentful Paint',
  TTFB: 'Time to First Byte',
  CLS: 'Cumulative Layout Shift',
  INP: 'Interaction to Next Paint',
}

const METRIC_UNIT: Record<string, string> = {
  LCP: 'ms',
  FCP: 'ms',
  TTFB: 'ms',
  CLS: '',
  INP: 'ms',
}

function ratingColor(good: number, ni: number, poor: number): string {
  const total = good + ni + poor
  if (total === 0) return 'var(--text-muted)'
  const goodPct = good / total
  if (goodPct >= 0.75) return '#22c55e'
  if (goodPct >= 0.5) return '#eab308'
  return '#ef4444'
}

function formatMetricValue(value: number, metric: string): string {
  if (metric === 'CLS') return value.toFixed(3)
  if (value >= 1000) return `${(value / 1000).toFixed(2)}s`
  return `${value.toFixed(0)}ms`
}

function ratingBar(good: number, ni: number, poor: number): ReactElement {
  const total = good + ni + poor
  if (total === 0) return <span style={{ color: 'var(--text-muted)' }}>-</span>
  const gPct = (good / total) * 100
  const nPct = (ni / total) * 100
  const pPct = (poor / total) * 100

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
      <div
        style={{
          display: 'flex',
          width: 80,
          height: 6,
          borderRadius: 3,
          overflow: 'hidden',
        }}
        title={`Good: ${good} (${gPct.toFixed(0)}%) | Needs improvement: ${ni} (${nPct.toFixed(0)}%) | Poor: ${poor} (${pPct.toFixed(0)}%)`}
      >
        {gPct > 0 && <div style={{ width: `${gPct}%`, background: '#22c55e' }} />}
        {nPct > 0 && <div style={{ width: `${nPct}%`, background: '#eab308' }} />}
        {pPct > 0 && <div style={{ width: `${pPct}%`, background: '#ef4444' }} />}
      </div>
      <span className="mono" style={{ fontSize: '0.6875rem', color: 'var(--text-muted)' }}>
        {total}
      </span>
    </div>
  )
}

type PageGroup = {
  page: string
  metrics: Map<string, WebVitalSummary>
}

type ServiceGroup = {
  service: string
  pages: PageGroup[]
}

export function PagesPage(): ReactElement {
  const navigate = useNavigate()
  const { range, setRange } = useTimeRange()
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [vitals, setVitals] = useState<WebVitalSummary[]>([])
  const [trends, setTrends] = useState<Map<string, number[]>>(new Map())
  const [loading, setLoading] = useState(true)
  const [search, setSearch] = useState('')
  const [serviceFilter, setServiceFilter] = useState('')

  const fetchData = useCallback(async () => {
    const { from, to } = getTimeRange(range)
    try {
      const data = await api.getWebVitals(from, to, serviceFilter || undefined)
      setVitals(data ?? [])
    } catch {
      // handled by client
    } finally {
      setLoading(false)
    }
  }, [range, serviceFilter])

  useEffect(() => {
    if (vitals.length === 0) return

    const { from, to } = getTimeRange(range)
    const seen = new Set<string>()
    const fetches: Promise<void>[] = []

    for (const v of vitals) {
      const key = `${v.service}:${v.page}:${v.metric}`
      if (seen.has(key)) continue
      seen.add(key)
      fetches.push(
        api.getWebVitalsTimeseries(v.page, v.metric, from, to, undefined, v.service || undefined).then((buckets: WebVitalTimeseriesBucket[]) => {
          setTrends((prev) => {
            const next = new Map(prev)
            next.set(key, buckets.map((b) => b.p50Ms))
            return next
          })
        }).catch(() => {}),
      )
    }

    void Promise.all(fetches)
  }, [vitals, range])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
    void fetchData()
  }, [fetchData])

  const services = useMemo(() => {
    const svcSet = new Set<string>()
    for (const v of vitals) svcSet.add(v.service)
    return Array.from(svcSet).sort()
  }, [vitals])

  const serviceGroups: ServiceGroup[] = useMemo(() => {
    const bySvc = new Map<string, Map<string, Map<string, WebVitalSummary>>>()
    for (const v of vitals) {
      let byPage = bySvc.get(v.service)
      if (!byPage) {
        byPage = new Map()
        bySvc.set(v.service, byPage)
      }
      let metrics = byPage.get(v.page)
      if (!metrics) {
        metrics = new Map()
        byPage.set(v.page, metrics)
      }
      metrics.set(v.metric, v)
    }

    const result: ServiceGroup[] = []
    for (const [service, byPage] of bySvc) {
      let pages: PageGroup[] = []
      for (const [page, metrics] of byPage) {
        pages.push({ page, metrics })
      }
      if (search) {
        const q = search.toLowerCase()
        pages = pages.filter((g) => g.page.toLowerCase().includes(q))
      }
      if (pages.length === 0) continue
      pages.sort((a, b) => a.page.localeCompare(b.page))
      result.push({ service, pages })
    }

    result.sort((a, b) => a.service.localeCompare(b.service))
    return result
  }, [vitals, search])

  return (
    <div>
      {/* Header */}
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginBottom: '1.5rem',
          flexWrap: 'wrap',
          gap: '0.75rem',
        }}
      >
        <h2 style={{ fontSize: '1.25rem', fontWeight: 700 }}>Pages</h2>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', flexWrap: 'wrap' }}>
          <div style={{ position: 'relative' }}>
            <Search
              size={14}
              style={{ position: 'absolute', left: 8, top: '50%', transform: 'translateY(-50%)', color: 'var(--text-muted)' }}
            />
            <input
              type="text"
              placeholder="Filter pages..."
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              style={{
                background: 'var(--surface)',
                border: '1px solid var(--border)',
                borderRadius: 6,
                padding: '6px 10px 6px 28px',
                color: 'var(--text)',
                fontSize: 13,
                outline: 'none',
                width: 180,
              }}
            />
          </div>
          {services.length > 1 && (
            <select
              value={serviceFilter}
              onChange={(e) => setServiceFilter(e.target.value)}
              style={{
                background: 'var(--surface)',
                border: '1px solid var(--border)',
                borderRadius: 6,
                padding: '6px 10px',
                color: 'var(--text)',
                fontSize: 13,
                outline: 'none',
              }}
            >
              <option value="">All services</option>
              {services.map((s) => (
                <option key={s} value={s}>{s}</option>
              ))}
            </select>
          )}
          <AutoRefresh value={refreshInterval} onChange={setRefreshInterval} onRefresh={fetchData} />
          <TimeRangeSelector value={range} onChange={setRange} />
        </div>
      </div>

      {loading ? (
        <div className="card" style={{ padding: '2rem', textAlign: 'center', color: 'var(--text-muted)' }}>
          Loading web vitals...
        </div>
      ) : serviceGroups.length === 0 ? (
        <div className="card" style={{ padding: '2rem', textAlign: 'center', color: 'var(--text-muted)' }}>
          {search ? 'No pages match your filter' : 'No web vitals data yet. Deploy the frontend SDK to start collecting.'}
        </div>
      ) : (
        serviceGroups.map((svcGroup) => (
          <div key={svcGroup.service} style={{ marginBottom: '1.5rem' }}>
            <h3 style={{ fontSize: '0.875rem', fontWeight: 600, color: 'var(--text-muted)', marginBottom: '0.5rem' }}>
              {svcGroup.service}
            </h3>
            {svcGroup.pages.map((group) => (
              <div
                key={group.page}
                className="card"
                style={{ marginBottom: '1rem', padding: 0, overflow: 'hidden', cursor: 'pointer' }}
                onClick={() => navigate(`/pages/${encodeURIComponent(group.page)}?service=${encodeURIComponent(svcGroup.service)}`)}
              >
                <div
                  style={{
                    padding: '0.75rem 1rem',
                    borderBottom: '1px solid var(--border)',
                    fontSize: '0.875rem',
                    fontWeight: 600,
                    color: 'var(--accent)',
                  }}
                  className="mono"
                >
                  {group.page}
                </div>
                <div style={{ overflowX: 'auto' }}>
                  <table>
                    <thead>
                      <tr>
                        <th style={{ textAlign: 'left' }}>Metric</th>
                        <th style={{ textAlign: 'left' }}>Trend</th>
                        <th style={{ textAlign: 'right' }}>P50</th>
                        <th style={{ textAlign: 'right' }}>P95</th>
                        <th style={{ textAlign: 'left' }}>Rating</th>
                        <th style={{ textAlign: 'right' }}>Samples</th>
                      </tr>
                    </thead>
                    <tbody>
                      {METRIC_ORDER.map((metric) => {
                        const v = group.metrics.get(metric)
                        if (!v) return null
                        const unit = METRIC_UNIT[metric]
                        const trendData = trends.get(`${svcGroup.service}:${group.page}:${metric}`)
                        return (
                          <tr key={metric}>
                            <td>
                              <span style={{ fontWeight: 600 }}>{metric}</span>
                              <span style={{ color: 'var(--text-muted)', fontSize: '0.75rem', marginLeft: '0.5rem' }}>
                                {METRIC_LABELS[metric]}
                              </span>
                              {unit && (
                                <span style={{ color: 'var(--text-muted)', fontSize: '0.6875rem', marginLeft: '0.25rem' }}>
                                  ({unit})
                                </span>
                              )}
                            </td>
                            <td>
                              <Sparkline
                                data={trendData ?? []}
                                color={ratingColor(v.good, v.needsImprovement, v.poor)}
                                fillColor={ratingColor(v.good, v.needsImprovement, v.poor)}
                              />
                            </td>
                            <td
                              style={{ textAlign: 'right', color: ratingColor(v.good, v.needsImprovement, v.poor) }}
                              className="mono"
                            >
                              {formatMetricValue(v.p50Ms, metric)}
                            </td>
                            <td style={{ textAlign: 'right' }} className="mono">
                              {formatMetricValue(v.p95Ms, metric)}
                            </td>
                            <td>{ratingBar(v.good, v.needsImprovement, v.poor)}</td>
                            <td style={{ textAlign: 'right' }} className="mono">
                              {v.samples}
                            </td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </table>
                </div>
              </div>
            ))}
          </div>
        ))
      )}
    </div>
  )
}
