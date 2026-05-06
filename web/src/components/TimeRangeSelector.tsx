import { type ReactElement } from 'react'
import { RANGES } from '../utils/timeRange'

type TimeRangeSelectorProps = {
  value: string
  onChange: (value: string) => void
}

export function TimeRangeSelector({ value, onChange }: TimeRangeSelectorProps): ReactElement {
  return (
    <div style={{ display: 'flex', gap: '0.25rem' }}>
      {RANGES.map((r) => (
        <button
          key={r.value}
          className={`btn btn-sm ${value === r.value ? 'btn-active' : ''}`}
          onClick={() => onChange(r.value)}
        >
          {r.label}
        </button>
      ))}
    </div>
  )
}
