import { useEffect, useRef, type ReactElement } from 'react'

type AutoRefreshOption = {
  label: string
  seconds: number
}

const OPTIONS: AutoRefreshOption[] = [
  { label: 'Off', seconds: 0 },
  { label: '10s', seconds: 10 },
  { label: '30s', seconds: 30 },
  { label: '1m', seconds: 60 },
]

type AutoRefreshProps = {
  value: number
  onChange: (seconds: number) => void
  onRefresh: () => void
}

export function AutoRefresh({ value, onChange, onRefresh }: AutoRefreshProps): ReactElement {
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null)

  useEffect(() => {
    if (intervalRef.current) {
      clearInterval(intervalRef.current)
      intervalRef.current = null
    }
    if (value > 0) {
      intervalRef.current = setInterval(onRefresh, value * 1000)
    }
    return () => {
      if (intervalRef.current) clearInterval(intervalRef.current)
    }
  }, [value, onRefresh])

  return (
    <select
      value={value}
      onChange={(e) => onChange(Number(e.target.value))}
      style={{
        width: 'auto',
        padding: '0.25rem 0.5rem',
        fontSize: '0.8125rem',
      }}
    >
      {OPTIONS.map((o) => (
        <option key={o.seconds} value={o.seconds}>
          {o.label}
        </option>
      ))}
    </select>
  )
}
