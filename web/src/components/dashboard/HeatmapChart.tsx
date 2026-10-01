import { type ReactElement } from 'react'
import type { DashboardHeatmap } from '../../api/dashboardTypes'
import { formatDuration } from '../../utils/format'
import { formatAxisTime, heatOpacity, layoutHeatmap } from '../../utils/dashboardData'

type HeatmapChartProps = {
  heatmap: DashboardHeatmap
  fromMs: number
  toMs: number
  label: string
  height?: number
}

const WIDTH = 600
const PAD = { left: 56, right: 8, top: 4, bottom: 20 }

/**
 * Duration heatmap drawn as an SVG grid: time across, duration up. Cell opacity
 * follows the span count on a log scale. Only populated cells are drawn.
 */
export function HeatmapChart({ heatmap, fromMs, toMs, label, height = 200 }: HeatmapChartProps): ReactElement {
  const layout = layoutHeatmap(heatmap.cells, fromMs, toMs, heatmap.intervalSeconds)
  const plotW = WIDTH - PAD.left - PAD.right
  const plotH = height - PAD.top - PAD.bottom

  return (
    <div>
      <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>{label}</div>
      <svg
        role="img"
        aria-label={label}
        viewBox={`0 0 ${WIDTH} ${height}`}
        preserveAspectRatio="none"
        style={{ width: '100%', height }}
      >
        {layout && <Cells layout={layout} plotW={plotW} plotH={plotH} />}
        {layout && <YTicks layout={layout} plotH={plotH} />}
        <XTicks fromMs={fromMs} toMs={toMs} plotW={plotW} y={height - 6} />
      </svg>
    </div>
  )
}

type LayoutProps = { layout: NonNullable<ReturnType<typeof layoutHeatmap>>; plotH: number }

function rowHeight(layout: LayoutProps['layout'], plotH: number): number {
  return plotH / (layout.maxBucket - layout.minBucket + 1)
}

function Cells({ layout, plotW, plotH }: LayoutProps & { plotW: number }): ReactElement {
  const cw = plotW / layout.cols
  const rh = rowHeight(layout, plotH)
  return (
    <g>
      {layout.cells.map((c) => (
        <rect
          key={`${c.col}-${c.bucket}`}
          x={PAD.left + c.col * cw}
          y={PAD.top + (layout.maxBucket - c.bucket) * rh}
          width={Math.max(cw, 1)}
          height={Math.max(rh, 1)}
          fill="var(--accent)"
          opacity={heatOpacity(c.count, layout.maxCount)}
        >
          <title>{`${c.count} spans, ${formatDuration(c.lowerUs)} to ${formatDuration(c.upperUs)}`}</title>
        </rect>
      ))}
    </g>
  )
}

function YTicks({ layout, plotH }: LayoutProps): ReactElement {
  const rh = rowHeight(layout, plotH)
  const rows = layout.maxBucket - layout.minBucket + 1
  const step = Math.max(1, Math.ceil(rows / 5))
  const ticks: { bucket: number; lowerUs: number }[] = []
  for (let b = layout.minBucket; b <= layout.maxBucket; b += step) {
    // Lower edge of bucket b, from the same half-octave scale the API uses.
    ticks.push({ bucket: b, lowerUs: Math.max(0, Math.round(Math.pow(2, b / 2)) - 1) })
  }
  return (
    <g fontSize="11" fill="var(--text-muted)">
      {ticks.map((t) => (
        <text key={t.bucket} x={PAD.left - 6} y={PAD.top + (layout.maxBucket - t.bucket + 1) * rh - 2} textAnchor="end">
          {formatDuration(t.lowerUs)}
        </text>
      ))}
    </g>
  )
}

function XTicks({ fromMs, toMs, plotW, y }: { fromMs: number; toMs: number; plotW: number; y: number }): ReactElement {
  const span = toMs - fromMs
  const count = 4
  return (
    <g fontSize="11" fill="var(--text-muted)">
      {Array.from({ length: count + 1 }, (_, i) => {
        const ms = fromMs + (span * i) / count
        const anchor = i === 0 ? 'start' : i === count ? 'end' : 'middle'
        return (
          <text key={i} x={PAD.left + (plotW * i) / count} y={y} textAnchor={anchor}>
            {formatAxisTime(ms, span)}
          </text>
        )
      })}
    </g>
  )
}
