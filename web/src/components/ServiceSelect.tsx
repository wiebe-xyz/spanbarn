import { useState, useEffect, type ReactElement } from 'react'
import { api } from '../api/client'
import { getTimeRange } from '../utils/timeRange'

type Props = {
  value: string
  onChange: (value: string) => void
  range: string
}

export function ServiceSelect({ value, onChange, range }: Props): ReactElement {
  const [services, setServices] = useState<string[]>([])

  useEffect(() => {
    const { from, to } = getTimeRange(range)
    api.getServices(from, to).then((data) => {
      setServices((data ?? []).map((s) => s.service))
    }).catch(() => {})
  }, [range])

  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      style={{
        padding: '0.25rem 0.5rem',
        fontSize: '0.8125rem',
        minWidth: 140,
      }}
    >
      <option value="">All services</option>
      {services.map((s) => (
        <option key={s} value={s}>{s}</option>
      ))}
    </select>
  )
}
