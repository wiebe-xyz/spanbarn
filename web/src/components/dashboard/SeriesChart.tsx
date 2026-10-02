import { type ReactElement } from 'react'
import { CartesianGrid, Line, LineChart, ReferenceArea, ReferenceLine, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { useBrush, xRange } from './useBrush'
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
  /** Release markers: a dashed vertical line with the version at the top. */
  markers?: { time: number; label: string }[]
  /** Drag across the chart to pick a time range; called with the range in epoch ms. */
  onBrush?: (fromMs: number, toMs: number) => void
  /** Makes each legend entry a button that reports its group. */
  onSelectGroup?: (group: string) => void
}

const tick = { fontSize: 11, fill: 'var(--text-muted)' }

/** One line per group over a fixed time window, so empty stretches stay empty. */
export function SeriesChart({ series, fromMs, toMs, label, formatValue, byStatus, height = 200, markers, onBrush, onSelectGroup }: SeriesChartProps): ReactElement {
  const span = toMs - fromMs
  const brush = useBrush((r) => onBrush?.(...xRange(r)))
  const labelX = (e: { activeLabel?: string | number }) => (e.activeLabel === undefined ? undefined : Number(e.activeLabel))
  const brushHandlers = onBrush
    ? {
        onMouseDown: (e: { activeLabel?: string | number }) => {
          const x = labelX(e)
          if (x !== undefined) brush.begin({ x })
        },
        onMouseMove: (e: { activeLabel?: string | number }) => {
          const x = labelX(e)
          if (x !== undefined) brush.extend({ x })
        },
        onMouseUp: brush.finish,
        onMouseLeave: brush.cancel,
      }
    : {}
  return (
    <div style={{ marginBottom: '0.75rem' }}>
      <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>{label}</div>
      <ResponsiveContainer width="100%" height={height}>
        <LineChart data={series.rows} {...brushHandlers} style={onBrush ? { cursor: 'crosshair', userSelect: 'none' } : undefined}>
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
          {markers?.map((m) => (
            <ReferenceLine
              key={`${m.time}-${m.label}`}
              x={m.time}
              stroke="var(--text-muted)"
              strokeDasharray="4 3"
              label={{ value: m.label, position: 'top', fontSize: 10, fill: 'var(--text-muted)' }}
            />
          ))}
          {brush.rect && (
            <ReferenceArea x1={brush.rect.start.x} x2={brush.rect.end.x} fill="var(--accent)" fillOpacity={0.2} stroke="var(--accent)" ifOverflow="hidden" />
          )}
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
      <Legend groups={series.groups} byStatus={byStatus} onSelect={onSelectGroup} />
    </div>
  )
}

function Legend({ groups, byStatus, onSelect }: { groups: string[]; byStatus?: boolean; onSelect?: (group: string) => void }): ReactElement {
  return (
    <ul style={{ display: 'flex', flexWrap: 'wrap', gap: '0.25rem 0.75rem', listStyle: 'none', padding: 0, margin: '0.25rem 0 0', fontSize: '0.75rem' }}>
      {groups.map((g, i) => (
        <li key={g} style={{ display: 'flex', alignItems: 'center', gap: 4, minWidth: 0 }}>
          <span aria-hidden="true" style={{ width: 10, height: 10, borderRadius: 2, flexShrink: 0, background: byStatus ? statusColor(g) : seriesColor(g, i) }} />
          {onSelect && g !== 'other' ? (
            <button
              type="button"
              className="btn-link"
              onClick={() => onSelect(g)}
              title={`Filter to ${g}`}
              style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', background: 'none', border: 0, padding: 0, color: 'inherit', font: 'inherit', cursor: 'pointer', textDecoration: 'underline dotted' }}
            >
              {g}
            </button>
          ) : (
            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={g}>{g}</span>
          )}
        </li>
      ))}
    </ul>
  )
}
