import { type ReactElement } from 'react'

type GroupSelectProps<T extends string> = {
  value: T
  options: { value: T; label: string }[]
  onChange: (value: T) => void
}

/** Compact "group by" select for a card header. */
export function GroupSelect<T extends string>({ value, options, onChange }: GroupSelectProps<T>): ReactElement {
  return (
    <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: '0.75rem', color: 'var(--text-muted)', marginBottom: '0.5rem' }}>
      Group by
      <select
        aria-label="Group by"
        value={value}
        onChange={(e) => onChange(e.target.value as T)}
        style={{ padding: '0.125rem 0.375rem', fontSize: '0.75rem' }}
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </label>
  )
}
