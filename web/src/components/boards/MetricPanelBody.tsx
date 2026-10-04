import { useEffect, useState, type ReactElement } from 'react'
import { api } from '../../api/client'
import type { MetricPanelQuery, MetricSeriesResponse } from '../../api/types'
import type { Window } from '../../boards/model'
import { MetricLines } from '../metrics/MetricLines'
import { RENDER_HINT } from '../../metrics/chart'
import { errorStyle, mutedText } from './styles'

type Props = {
  projectId: number
  metric: MetricPanelQuery
  win: Window
  /** Changes when the board refreshes, so the series loads again. */
  tick: number
}

/** A metric panel: one OTLP metric of the board's project over the board's window. */
export function MetricPanelBody({ projectId, metric, win, tick }: Props): ReactElement {
  const [resp, setResp] = useState<MetricSeriesResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const groupKey = (metric.groupBy ?? []).join(',')

  useEffect(() => {
    let cancelled = false
    // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
    setError(null)
    api
      .getMetricSeries(
        metric.name,
        new Date(win.fromMs).toISOString(),
        new Date(win.toMs).toISOString(),
        undefined,
        undefined,
        projectId,
        metric.groupBy,
      )
      .then((r) => { if (!cancelled) setResp(r ?? null) })
      .catch((e: unknown) => { if (!cancelled) setError(e instanceof Error ? e.message : 'Query failed') })
    return () => { cancelled = true }
    // metric.groupBy is compared through groupKey: a new array with the same keys is the same query.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId, metric.name, groupKey, win.fromMs, win.toMs, tick])

  if (error) return <div role="alert" style={errorStyle}>{error}</div>
  if (!resp) return <p style={mutedText}>Loading chart...</p>
  const points = resp.series.reduce((n, s) => n + s.points.length, 0)
  return (
    <div>
      <p style={{ ...mutedText, margin: '0 0 6px', fontSize: 12 }}>
        <span style={{ fontFamily: 'monospace' }}>{resp.name}</span>
        {resp.unit ? ` · ${resp.unit}` : ''} · {RENDER_HINT[resp.render] ?? resp.render}
      </p>
      {points === 0 ? <p style={mutedText}>No data points in this range</p> : <MetricLines resp={resp} height={220} />}
    </div>
  )
}
