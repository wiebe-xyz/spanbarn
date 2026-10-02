import { type ReactElement } from 'react'
import { X, ZoomOut } from 'lucide-react'
import { formatDuration } from '../../utils/format'

export type ChipKey = 'service' | 'name' | 'status' | 'band'

export type ActiveFiltersProps = {
  /** The zoomed window, or null on a preset range. */
  zoom: { fromMs: number; toMs: number } | null
  minUs: number
  maxUs: number
  service: string
  name: string
  status: string
  onZoomOut: () => void
  onResetZoom: () => void
  onClear: (key: ChipKey) => void
  onClearAll: () => void
}

const when = (ms: number) =>
  new Date(ms).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })

function bandLabel(minUs: number, maxUs: number): string {
  if (minUs > 0 && maxUs > 0) return `duration ${formatDuration(minUs)} to ${formatDuration(maxUs)}`
  if (minUs > 0) return `duration at least ${formatDuration(minUs)}`
  return `duration up to ${formatDuration(maxUs)}`
}

/** What narrows the dashboard right now, each piece removable, plus the zoom controls. */
export function ActiveFilters(p: ActiveFiltersProps): ReactElement | null {
  const chips: { key: ChipKey; text: string }[] = []
  if (p.service) chips.push({ key: 'service', text: `service = ${p.service}` })
  if (p.name) chips.push({ key: 'name', text: `span = ${p.name}` })
  if (p.status) chips.push({ key: 'status', text: `status = ${p.status}` })
  if (p.minUs > 0 || p.maxUs > 0) chips.push({ key: 'band', text: bandLabel(p.minUs, p.maxUs) })
  if (!p.zoom && chips.length === 0) return null

  return (
    <div
      role="group"
      aria-label="Active zoom and filters"
      style={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: '0.5rem', marginBottom: '1rem', fontSize: '0.8125rem' }}
    >
      {p.zoom && (
        <>
          <span style={{ fontWeight: 600 }}>
            Zoomed {when(p.zoom.fromMs)} to {when(p.zoom.toMs)}
          </span>
          <button type="button" className="btn btn-sm" onClick={p.onZoomOut}>
            <ZoomOut size={14} aria-hidden="true" /> Zoom out
          </button>
          <button type="button" className="btn btn-sm" onClick={p.onResetZoom}>
            Reset zoom
          </button>
        </>
      )}
      {chips.map((c) => (
        <button
          key={c.key}
          type="button"
          className="btn btn-sm"
          aria-label={`Remove ${c.text}`}
          onClick={() => p.onClear(c.key)}
          style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}
        >
          {c.text}
          <X size={12} aria-hidden="true" />
        </button>
      ))}
      {chips.length > 1 && (
        <button type="button" className="btn btn-sm" onClick={p.onClearAll}>
          Clear all filters
        </button>
      )}
    </div>
  )
}
