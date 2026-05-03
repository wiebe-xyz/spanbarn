import { type ReactElement } from 'react'

type RangeOption = {
  label: string
  value: string
  hours: number
}

const RANGES: RangeOption[] = [
  { label: '1h', value: '1h', hours: 1 },
  { label: '4h', value: '4h', hours: 4 },
  { label: '24h', value: '24h', hours: 24 },
  { label: '7d', value: '7d', hours: 168 },
  { label: '30d', value: '30d', hours: 720 },
]

export type TimeRange = {
  from: string
  to: string
  label: string
}

export function getTimeRange(value: string): TimeRange {
  const range = RANGES.find((r) => r.value === value) ?? RANGES[0]
  const to = new Date()
  const from = new Date(to.getTime() - range.hours * 3600_000)
  return {
    from: from.toISOString(),
    to: to.toISOString(),
    label: range.value,
  }
}

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
