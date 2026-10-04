import { useMemo, type ReactElement } from 'react'
import { LineChart, Line, CartesianGrid, XAxis, YAxis, Tooltip, Legend, ResponsiveContainer } from 'recharts'
import type { MetricSeriesResponse } from '../../api/types'
import { buildChart } from '../../metrics/chart'

/** A metric series response drawn as lines, as the Metrics page and metric board panels show it. */
export function MetricLines({ resp, height }: { resp: MetricSeriesResponse; height: number }): ReactElement {
  const { rows, lines } = useMemo(() => buildChart(resp), [resp])
  const unit = resp.unit
  return (
    <ResponsiveContainer width="100%" height={height}>
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
          formatter={(value) => [`${Number(value).toLocaleString()}${unit ? ` ${unit}` : ''}`]}
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
  )
}
