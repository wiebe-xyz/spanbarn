import { useState, type FormEvent, type ReactElement } from 'react'
import { FilterBuilder } from '../filter/FilterBuilder'
import { buttonStyle, errorStyle, inputStyle, mutedText, primaryButton } from '../boards/styles'
import { emptyExpr, parseFilter, serializeFilter, type FilterExpr } from '../../filters/model'
import { validateSlo } from './validate'
import { filterBody, type Slo, type SloSettings } from '../../api/slos'

type Props = {
  projectId: number
  /** The SLO being edited, or null to create one. */
  slo: Slo | null
  onSubmit: (s: SloSettings) => Promise<void>
  onCancel: () => void
}

/** Strips float noise from the percent form of a fraction, so 0.995 shows as 99.5. */
const toPercent = (fraction: number) => String(Number((fraction * 100).toFixed(6)))

const formStyle = {
  background: '#111827', border: '1px solid #1f2937', borderRadius: 8, padding: 16,
  display: 'grid', gap: 12, maxWidth: 720, marginBottom: 16,
} as const

export function SloForm({ projectId, slo, onSubmit, onCancel }: Props): ReactElement {
  const [name, setName] = useState(slo?.name ?? '')
  const [targetPercent, setTargetPercent] = useState(slo ? toPercent(slo.target) : '99.5')
  const [windowDays, setWindowDays] = useState(slo ? String(slo.windowDays) : '30')
  const [good, setGood] = useState<FilterExpr>(slo ? parseFilter(slo.goodFilter) : emptyExpr())
  const [total, setTotal] = useState<FilterExpr>(slo ? parseFilter(slo.totalFilter) : emptyExpr())
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const problem = validateSlo({ name, targetPercent, windowDays })
    if (problem) return setError(problem)
    setError(null)
    setSaving(true)
    onSubmit({
      name: name.trim(),
      goodFilter: filterBody(serializeFilter(good)),
      totalFilter: filterBody(serializeFilter(total)),
      target: Number((Number(targetPercent) / 100).toFixed(8)),
      windowDays: Number(windowDays),
    })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'Could not save the SLO'))
      .finally(() => setSaving(false))
  }

  const field = { display: 'grid', gap: 4, ...mutedText } as const
  return (
    <form onSubmit={submit} aria-label={slo ? 'Edit SLO' : 'New SLO'} style={formStyle}>
      <h2 style={{ fontSize: 16, margin: 0 }}>{slo ? 'Edit SLO' : 'New SLO'}</h2>
      {error && <div role="alert" style={{ ...errorStyle, marginBottom: 0 }}>{error}</div>}
      <label style={field}>
        Name
        <input value={name} maxLength={120} onChange={(e) => setName(e.target.value)} style={inputStyle} />
      </label>
      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
        <label style={field}>
          Target (%)
          <input inputMode="decimal" value={targetPercent} onChange={(e) => setTargetPercent(e.target.value)} style={{ ...inputStyle, width: 110 }} />
        </label>
        <label style={field}>
          Window (days)
          <input inputMode="numeric" value={windowDays} onChange={(e) => setWindowDays(e.target.value)} style={{ ...inputStyle, width: 110 }} />
        </label>
      </div>
      <p style={{ ...mutedText, margin: 0 }}>
        The SLO is the share of spans matching the good filter among those matching the total filter. An empty filter matches every span.
      </p>
      <FilterBuilder value={good} onChange={setGood} projectId={projectId} label="Good events" />
      <FilterBuilder value={total} onChange={setTotal} projectId={projectId} label="Total events" />
      <div style={{ display: 'flex', gap: 8 }}>
        <button type="submit" style={primaryButton} disabled={saving}>{slo ? 'Save SLO' : 'Create SLO'}</button>
        <button type="button" style={buttonStyle} onClick={onCancel}>Cancel</button>
      </div>
    </form>
  )
}
