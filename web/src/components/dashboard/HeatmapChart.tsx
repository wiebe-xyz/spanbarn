import { useRef, type ReactElement, type PointerEvent } from 'react'
import type { DashboardHeatmap } from '../../api/dashboardTypes'
import { formatDuration } from '../../utils/format'
import { formatAxisTime, heatOpacity, layoutHeatmap } from '../../utils/dashboardData'
import { useBrush, xRange, type BrushRect } from './useBrush'

type HeatmapChartProps = {
  heatmap: DashboardHeatmap
  fromMs: number
  toMs: number
  label: string
  height?: number
  /**
   * Drag a rectangle to pick a time range and a duration band. minUs/maxUs are
   * both 0 when the drag spans (almost) the whole height, which means time only.
   */
  onBrush?: (sel: HeatmapSelection) => void
}

export type HeatmapSelection = { fromMs: number; toMs: number; minUs: number; maxUs: number }

const WIDTH = 600
const PAD = { left: 56, right: 8, top: 4, bottom: 20 }

/**
 * Duration heatmap drawn as an SVG grid: time across, duration up. Cell opacity
 * follows the span count on a log scale. Only populated cells are drawn.
 */
export function HeatmapChart({ heatmap, fromMs, toMs, label, height = 200, onBrush }: HeatmapChartProps): ReactElement {
  const layout = layoutHeatmap(heatmap.cells, fromMs, toMs, heatmap.intervalSeconds)
  const plotW = WIDTH - PAD.left - PAD.right
  const plotH = height - PAD.top - PAD.bottom
  const svgRef = useRef<SVGSVGElement>(null)
  const brush = useBrush((r) => {
    if (layout) onBrush?.(toSelection(r, layout, { fromMs, toMs, plotW, plotH }))
  })

  // Pointer position in viewBox units, so the selection tracks the scaled svg.
  const point = (e: PointerEvent<SVGSVGElement>) => {
    const box = svgRef.current?.getBoundingClientRect()
    if (!box || box.width === 0 || box.height === 0) return null
    const vx = Math.min(PAD.left + plotW, Math.max(PAD.left, ((e.clientX - box.left) / box.width) * WIDTH))
    const vy = Math.min(PAD.top + plotH, Math.max(PAD.top, ((e.clientY - box.top) / box.height) * height))
    return { x: fromMs + ((vx - PAD.left) / plotW) * (toMs - fromMs), y: vy }
  }
  const brushProps = onBrush && layout
    ? {
        onPointerDown: (e: PointerEvent<SVGSVGElement>) => {
          const p = point(e)
          if (!p) return
          e.currentTarget.setPointerCapture?.(e.pointerId)
          brush.begin(p)
        },
        onPointerMove: (e: PointerEvent<SVGSVGElement>) => {
          const p = point(e)
          if (p) brush.extend(p)
        },
        onPointerUp: brush.finish,
        onPointerCancel: brush.cancel,
      }
    : {}

  return (
    <div>
      <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>{label}</div>
      <svg
        ref={svgRef}
        {...brushProps}
        role="img"
        aria-label={label}
        viewBox={`0 0 ${WIDTH} ${height}`}
        preserveAspectRatio="none"
        style={{ width: '100%', height, cursor: onBrush ? 'crosshair' : undefined, touchAction: onBrush ? 'none' : undefined, userSelect: 'none' }}
      >
        {layout && <Cells layout={layout} plotW={plotW} plotH={plotH} />}
        {layout && <YTicks layout={layout} plotH={plotH} />}
        {brush.rect && <Selection rect={brush.rect} fromMs={fromMs} toMs={toMs} plotW={plotW} />}
        <XTicks fromMs={fromMs} toMs={toMs} plotW={plotW} y={height - 6} />
      </svg>
    </div>
  )
}

type Layout = NonNullable<ReturnType<typeof layoutHeatmap>>
type Geometry = { fromMs: number; toMs: number; plotW: number; plotH: number }

/** Lower duration edge, in microseconds, of the (fractional) half-octave bucket b. */
function bucketLowerUs(b: number): number {
  return Math.max(0, Math.round(Math.pow(2, b / 2)) - 1)
}

/** Turns a dragged rectangle (time in ms, y in viewBox units) into a time range and duration band. */
function toSelection(r: BrushRect, layout: Layout, g: Geometry): HeatmapSelection {
  const [fromMs, toMs] = xRange(r)
  const ys = [r.start.y ?? 0, r.end.y ?? 0].sort((a, b) => a - b)
  if ((ys[1] - ys[0]) / g.plotH >= 0.9) return { fromMs, toMs, minUs: 0, maxUs: 0 }
  const rows = layout.maxBucket - layout.minBucket + 1
  // y grows downward; the top row is maxBucket.
  const bucketAt = (vy: number) => layout.maxBucket + 1 - ((vy - PAD.top) / g.plotH) * rows
  const lowB = Math.floor(bucketAt(ys[1]))
  const highB = Math.floor(bucketAt(ys[0]))
  return { fromMs, toMs, minUs: bucketLowerUs(lowB), maxUs: bucketLowerUs(highB + 1) }
}

function Selection({ rect, fromMs, toMs, plotW }: { rect: BrushRect; fromMs: number; toMs: number; plotW: number }): ReactElement {
  const px = (ms: number) => PAD.left + ((ms - fromMs) / (toMs - fromMs)) * plotW
  const [a, b] = xRange(rect)
  const ys = [rect.start.y ?? 0, rect.end.y ?? 0].sort((p, q) => p - q)
  return (
    <rect x={px(a)} y={ys[0]} width={px(b) - px(a)} height={ys[1] - ys[0]} fill="var(--accent)" fillOpacity={0.2} stroke="var(--accent)" pointerEvents="none" data-testid="heatmap-selection" />
  )
}

type LayoutProps = { layout: Layout; plotH: number }

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
