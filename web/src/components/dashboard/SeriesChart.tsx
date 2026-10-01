import { type ReactElement } from 'react'
import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { formatAxisTime, seriesColor, statusColor, statusDash, type PivotedSeries } from '../../utils/dashboardData'

type SeriesChartProps = {
  series: PivotedSeries
  fromMs: number
  toMs: number
  /** Label shown above the chart, e.g. "P99(duration_ms)". */
  label: string
  formatValue: (v: number) => string
  /** Colour series by HTTP status class instead of the shared palette. */
  byStatus?: boolean
  height?: number
}

const tick = { fontSize: 11, fill: 'var(--text-muted)' }

/** One line per group over a fixed time window, so empty stretches stay empty. */
export function SeriesChart({ series, fromMs, toMs, label, formatValue, byStatus, height = 200 }: SeriesChartProps): ReactElement {
  const span = toMs - fromMs
  return (
    <div style={{ marginBottom: '0.75rem' }}>
      <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>{label}</div>
      <ResponsiveContainer width="100%" height={height}>
        <LineChart data={series.rows}>
          <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
          <XAxis
            dataKey="time"
            type="number"
            scale="time"
            domain={[fromMs, toMs]}
            tick={tick}
            tickFormatter={(v: number) => formatAxisTime(v, span)}
          />
          <YAxis tick={tick} tickFormatter={formatValue} width={56} />
          <Tooltip
            labelFormatter={(v) => new Date(Number(v)).toLocaleString()}
            formatter={(v) => formatValue(Number(v))}
            contentStyle={{
              background: 'var(--surface)',
              border: '1px solid var(--border)',
              borderRadius: 8,
              color: 'var(--text)',
            }}
          />
          {series.groups.map((g, i) => (
            <Line
              key={g}
              name={g}
              type="monotone"
              dataKey={(row: { values: Record<string, number> }) => row.values[g]}
              stroke={byStatus ? statusColor(g) : seriesColor(g, i)}
              strokeDasharray={byStatus ? statusDash(g, series.groups) : undefined}
              strokeWidth={1.5}
              dot={false}
              connectNulls={false}
              isAnimationActive={false}
            />
          ))}
        </LineChart>
      </ResponsiveContainer>
      <Legend groups={series.groups} byStatus={byStatus} />
    </div>
  )
}

function Legend({ groups, byStatus }: { groups: string[]; byStatus?: boolean }): ReactElement {
  return (
    <ul style={{ display: 'flex', flexWrap: 'wrap', gap: '0.25rem 0.75rem', listStyle: 'none', padding: 0, margin: '0.25rem 0 0', fontSize: '0.75rem' }}>
      {groups.map((g, i) => (
        <li key={g} style={{ display: 'flex', alignItems: 'center', gap: 4, minWidth: 0 }}>
          <span aria-hidden="true" style={{ width: 10, height: 10, borderRadius: 2, flexShrink: 0, background: byStatus ? statusColor(g) : seriesColor(g, i) }} />
          <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={g}>{g}</span>
        </li>
      ))}
    </ul>
  )
}
