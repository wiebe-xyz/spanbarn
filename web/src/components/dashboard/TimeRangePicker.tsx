import { type ReactElement } from 'react'
import { ChevronLeft, ChevronRight, Clock } from 'lucide-react'
import { DASHBOARD_RANGES } from '../../utils/dashboardWindow'

type TimeRangePickerProps = {
  range: string
  /** 0 is the window ending now; each step moves back by one window width. */
  offset: number
  /** A zoomed window is showing; the select then reads Custom and the arrows are off. */
  custom?: boolean
  onRangeChange: (range: string) => void
  onOffsetChange: (offset: number) => void
}

/** Range select plus arrows that move the window back or forward by its own width. */
export function TimeRangePicker({ range, offset, custom = false, onRangeChange, onOffsetChange }: TimeRangePickerProps): ReactElement {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
      <label style={{ display: 'flex', alignItems: 'center', gap: '0.375rem', fontSize: '0.8125rem' }}>
        <Clock size={14} aria-hidden="true" />
        <select
          aria-label="Time range"
          value={custom ? 'custom' : range}
          onChange={(e) => onRangeChange(e.target.value)}
          style={{ padding: '0.25rem 0.5rem', fontSize: '0.8125rem' }}
        >
          {custom && <option value="custom" disabled>Custom (zoomed)</option>}
          {DASHBOARD_RANGES.map((r) => (
            <option key={r.value} value={r.value}>{r.label}</option>
          ))}
        </select>
      </label>
      <button
        className="btn btn-sm"
        aria-label="Previous window"
        disabled={custom}
        onClick={() => onOffsetChange(offset + 1)}
      >
        <ChevronLeft size={16} aria-hidden="true" />
      </button>
      <button
        className="btn btn-sm"
        aria-label="Next window"
        disabled={custom || offset === 0}
        onClick={() => onOffsetChange(Math.max(0, offset - 1))}
      >
        <ChevronRight size={16} aria-hidden="true" />
      </button>
    </div>
  )
}
