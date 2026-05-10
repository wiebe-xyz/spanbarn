import { useState, useEffect, useCallback, type ReactElement } from 'react'
import { useParams, Link } from 'react-router-dom'
import { ChevronRight } from 'lucide-react'
import {
  LineChart,
  Line,
  AreaChart,
  Area,
  XAxis,
  YAxis,
  Tooltip,
  Legend,
  ResponsiveContainer,
  CartesianGrid,
} from 'recharts'
import { api } from '../api/client'
import type { WebVitalSummary, WebVitalTimeseriesBucket } from '../api/types'
import { TimeRangeSelector } from '../components/TimeRangeSelector'
import { getTimeRange } from '../utils/timeRange'
import { useTimeRange } from '../contexts/useTimeRange'

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

function formatMetricValue(value: number, metric: string): string {
  if (metric === 'CLS') return value.toFixed(3)
  if (value >= 1000) return `${(value / 1000).toFixed(2)}s`
  return `${value.toFixed(0)}ms`
}

function ratingColor(good: number, ni: number, poor: number): string {
  const total = good + ni + poor
  if (total === 0) return 'var(--text-muted)'
  const goodPct = good / total
  if (goodPct >= 0.75) return '#22c55e'
  if (goodPct >= 0.5) return '#eab308'
  return '#ef4444'
}

type MetricTimeseries = {
  metric: string
  summary: WebVitalSummary | undefined
  buckets: WebVitalTimeseriesBucket[]
}

export function PageDetailPage(): ReactElement {
  const { page } = useParams<{ page: string }>()
  const decodedPage = decodeURIComponent(page ?? '/')
  const { range, setRange } = useTimeRange()
  const [metrics, setMetrics] = useState<MetricTimeseries[]>([])
  const [loading, setLoading] = useState(true)

  const fetchData = useCallback(async () => {
    if (!page) return
    const { from, to } = getTimeRange(range)
    try {
      const [vitals, ...tsResults] = await Promise.all([
        api.getWebVitals(from, to),
        ...METRIC_ORDER.map((m) => api.getWebVitalsTimeseries(decodedPage, m, from, to)),
      ])

      const summaries = (vitals ?? []).filter((v: WebVitalSummary) => v.page === decodedPage)
      const summaryMap = new Map(summaries.map((v: WebVitalSummary) => [v.metric, v]))

      setMetrics(
        METRIC_ORDER.map((m, i) => ({
          metric: m,
          summary: summaryMap.get(m),
          buckets: (tsResults[i] as WebVitalTimeseriesBucket[]) ?? [],
        })).filter((m) => m.summary || m.buckets.length > 0),
      )
    } catch {
      // handled by client
    } finally {
      setLoading(false)
    }
  }, [page, decodedPage, range])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
    void fetchData()
  }, [fetchData])

  if (loading) {
    return (
      <div>
        <div className="skeleton" style={{ height: 24, width: 300, marginBottom: 24 }} />
        {METRIC_ORDER.map((m) => (
          <div key={m} className="skeleton" style={{ height: 200, marginBottom: 16 }} />
        ))}
      </div>
    )
  }

  return (
    <div>
      <div className="breadcrumb">
        <Link to="/pages">Pages</Link>
        <ChevronRight size={14} />
        <span style={{ color: 'var(--text)' }}>{decodedPage}</span>
      </div>

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
        <h2 className="mono" style={{ fontSize: '1.25rem', fontWeight: 700 }}>{decodedPage}</h2>
        <TimeRangeSelector value={range} onChange={setRange} />
      </div>

      {metrics.length === 0 ? (
        <div className="card" style={{ padding: '2rem', textAlign: 'center', color: 'var(--text-muted)' }}>
          No web vitals data for this page in the selected time range.
        </div>
      ) : (
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '1rem' }}>
          {metrics.map(({ metric, summary, buckets }) => {
            const unit = METRIC_UNIT[metric]
            const isCLS = metric === 'CLS'
            const color = summary
              ? ratingColor(summary.good, summary.needsImprovement, summary.poor)
              : 'var(--accent)'

            const chartData = buckets.map((b) => ({
              time: new Date(b.bucket).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
              p50: isCLS ? b.p50Ms : b.p50Ms,
              p95: isCLS ? b.p95Ms : b.p95Ms,
              samples: b.samples,
              goodPct: b.samples > 0 ? (b.good / b.samples) * 100 : 0,
              niPct: b.samples > 0 ? (b.needsImprovement / b.samples) * 100 : 0,
              poorPct: b.samples > 0 ? (b.poor / b.samples) * 100 : 0,
            }))

            return (
              <div key={metric} className="card">
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: '0.75rem' }}>
                  <div>
                    <span style={{ fontWeight: 700, fontSize: '0.9375rem' }}>{metric}</span>
                    <span style={{ color: 'var(--text-muted)', fontSize: '0.75rem', marginLeft: '0.5rem' }}>
                      {METRIC_LABELS[metric]}
                    </span>
                  </div>
                  {summary && (
                    <div style={{ display: 'flex', gap: '1rem', fontSize: '0.8125rem' }}>
                      <span>
                        <span style={{ color: 'var(--text-muted)' }}>p50 </span>
                        <span className="mono" style={{ color, fontWeight: 600 }}>
                          {formatMetricValue(summary.p50Ms, metric)}
                        </span>
                      </span>
                      <span>
                        <span style={{ color: 'var(--text-muted)' }}>p95 </span>
                        <span className="mono" style={{ fontWeight: 600 }}>
                          {formatMetricValue(summary.p95Ms, metric)}
                        </span>
                      </span>
                      {unit && (
                        <span style={{ color: 'var(--text-muted)', fontSize: '0.6875rem' }}>({unit})</span>
                      )}
                    </div>
                  )}
                </div>

                {chartData.length > 0 ? (
                  <>
                    <ResponsiveContainer width="100%" height={160}>
                      <LineChart data={chartData} margin={{ top: 4, right: 8, bottom: 0, left: -16 }}>
                        <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
                        <XAxis dataKey="time" tick={{ fontSize: 10, fill: 'var(--text-muted)' }} />
                        <YAxis tick={{ fontSize: 10, fill: 'var(--text-muted)' }} width={40} />
                        <Tooltip
                          contentStyle={{
                            background: 'var(--surface)',
                            border: '1px solid var(--border)',
                            borderRadius: 8,
                            color: 'var(--text)',
                          }}
                          formatter={(value: number) => [isCLS ? value.toFixed(3) : `${value.toFixed(1)}ms`]}
                        />
                        <Legend verticalAlign="top" align="right" iconType="line" wrapperStyle={{ fontSize: 10, paddingBottom: 4 }} />
                        <Line type="monotone" dataKey="p95" name="p95" stroke="#eab308" dot={false} strokeWidth={1.5} />
                        <Line type="monotone" dataKey="p50" name="p50" stroke={color} dot={false} strokeWidth={2} />
                      </LineChart>
                    </ResponsiveContainer>

                    <div style={{ marginTop: '0.5rem' }}>
                      <ResponsiveContainer width="100%" height={60}>
                        <AreaChart data={chartData} margin={{ top: 0, right: 8, bottom: 0, left: -16 }}>
                          <XAxis dataKey="time" hide />
                          <YAxis domain={[0, 100]} hide />
                          <Tooltip
                            contentStyle={{
                              background: 'var(--surface)',
                              border: '1px solid var(--border)',
                              borderRadius: 8,
                              color: 'var(--text)',
                              fontSize: 11,
                            }}
                            formatter={(value: number) => [`${value.toFixed(0)}%`]}
                          />
                          <Area type="monotone" dataKey="goodPct" name="Good" stackId="1" stroke="none" fill="#22c55e" fillOpacity={0.6} />
                          <Area type="monotone" dataKey="niPct" name="Needs improvement" stackId="1" stroke="none" fill="#eab308" fillOpacity={0.6} />
                          <Area type="monotone" dataKey="poorPct" name="Poor" stackId="1" stroke="none" fill="#ef4444" fillOpacity={0.6} />
                        </AreaChart>
                      </ResponsiveContainer>
                    </div>
                  </>
                ) : (
                  <div style={{ padding: '2rem', textAlign: 'center', color: 'var(--text-muted)', fontSize: '0.8125rem' }}>
                    No timeseries data
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
