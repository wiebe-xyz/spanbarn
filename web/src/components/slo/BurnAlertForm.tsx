import { useState, type FormEvent, type ReactElement } from 'react'
import { buttonStyle, errorStyle, inputStyle, mutedText, primaryButton } from '../boards/styles'
import { validateBurnAlert, type BurnAlertValues } from './validate'
import type { BurnAlert, BurnAlertSettings } from '../../api/slos'

type Props = {
  windowDays: number
  alert: BurnAlert | null
  onSubmit: (a: BurnAlertSettings) => Promise<void>
  onCancel: () => void
}

const formStyle = { display: 'grid', gap: 10, padding: 12, background: '#0b1220', borderRadius: 6, marginTop: 8 } as const

export function BurnAlertForm({ windowDays, alert, onSubmit, onCancel }: Props): ReactElement {
  const [v, setV] = useState<BurnAlertValues>({
    windowMinutes: alert ? String(alert.windowMinutes) : '60',
    burnRate: alert ? String(alert.burnRate) : '14.4',
    email: alert?.email ?? '',
    webhookUrl: alert?.webhookUrl ?? '',
    cooldownMinutes: alert ? String(alert.cooldownMinutes) : '',
  })
  const [enabled, setEnabled] = useState(alert?.enabled ?? true)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const set = (k: keyof BurnAlertValues) => (e: { target: { value: string } }) => setV({ ...v, [k]: e.target.value })

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const problem = validateBurnAlert(v, windowDays)
    if (problem) return setError(problem)
    setError(null)
    setSaving(true)
    onSubmit({
      windowMinutes: Number(v.windowMinutes),
      burnRate: Number(v.burnRate),
      email: v.email.trim(),
      webhookUrl: v.webhookUrl.trim(),
      cooldownMinutes: Number(v.cooldownMinutes || 0),
      enabled,
    })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'Could not save the burn alert'))
      .finally(() => setSaving(false))
  }

  const field = { display: 'grid', gap: 4, ...mutedText } as const
  return (
    <form onSubmit={submit} aria-label={alert ? 'Edit burn alert' : 'New burn alert'} style={formStyle}>
      {error && <div role="alert" style={{ ...errorStyle, marginBottom: 0 }}>{error}</div>}
      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
        <label style={field}>
          Alert window (minutes)
          <input inputMode="numeric" value={v.windowMinutes} onChange={set('windowMinutes')} style={{ ...inputStyle, width: 120 }} />
        </label>
        <label style={field}>
          Burn rate
          <input inputMode="decimal" value={v.burnRate} onChange={set('burnRate')} style={{ ...inputStyle, width: 90 }} />
        </label>
        <label style={field}>
          Cooldown (minutes)
          <input inputMode="numeric" placeholder="30" value={v.cooldownMinutes} onChange={set('cooldownMinutes')} style={{ ...inputStyle, width: 110 }} />
        </label>
      </div>
      <label style={field}>
        Email
        <input value={v.email} onChange={set('email')} style={inputStyle} />
      </label>
      <label style={field}>
        Webhook URL
        <input value={v.webhookUrl} onChange={set('webhookUrl')} style={inputStyle} />
      </label>
      <label style={{ ...mutedText, display: 'flex', gap: 6, alignItems: 'center' }}>
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} /> Enabled
      </label>
      <div style={{ display: 'flex', gap: 8 }}>
        <button type="submit" style={primaryButton} disabled={saving}>{alert ? 'Save alert' : 'Add alert'}</button>
        <button type="button" style={buttonStyle} onClick={onCancel}>Cancel</button>
      </div>
    </form>
  )
}
