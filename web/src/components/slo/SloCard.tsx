import { useState, type ReactElement } from 'react'
import { buttonStyle, errorStyle, iconButton, mutedText } from '../boards/styles'
import { sloApi, type BurnAlert, type BurnAlertSettings, type Slo, type SloStatus } from '../../api/slos'
import { formatWindow } from './validate'
import { BurnAlertForm } from './BurnAlertForm'

type Props = {
  projectId: number
  slo: Slo
  status: SloStatus | null
  alerts: BurnAlert[]
  onEdit: () => void
  onChanged: () => void
}

const pct = (fraction: number, digits = 1) => `${(fraction * 100).toFixed(digits)}%`
const fmt = (n: number) => n.toLocaleString('en-US')

/** The budget bar. A negative budget fills the bar in red and is named as overspent. */
function BudgetBar({ remaining }: { remaining: number }): ReactElement {
  const over = remaining < 0
  const fill = Math.max(0, Math.min(1, remaining))
  const color = over ? '#f87171' : remaining < 0.25 ? '#fbbf24' : '#34d399'
  const label = over ? `Overspent by ${pct(-remaining)}` : `${pct(remaining)} of the budget remaining`
  return (
    <div>
      <div
        role="progressbar"
        aria-label="Error budget remaining"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(fill * 100)}
        style={{ height: 10, background: '#1f2937', borderRadius: 5, overflow: 'hidden' }}
      >
        <div style={{ width: over ? '100%' : `${fill * 100}%`, height: '100%', background: color }} />
      </div>
      <div style={{ color, fontSize: 13, marginTop: 4, fontWeight: 600 }}>{label}</div>
    </div>
  )
}

const cardStyle = { background: '#111827', border: '1px solid #1f2937', borderRadius: 8, padding: 16, display: 'grid', gap: 10 } as const

export function SloCard({ projectId, slo, status, alerts, onEdit, onChanged }: Props): ReactElement {
  const [editing, setEditing] = useState<BurnAlert | 'new' | null>(null)
  const [error, setError] = useState<string | null>(null)

  const run = (p: Promise<unknown>, fallback: string) =>
    p.then(() => { setError(null); onChanged() }).catch((e: unknown) => setError(e instanceof Error ? e.message : fallback))

  const saveAlert = async (a: BurnAlertSettings) => {
    if (editing === 'new') await sloApi.createAlert(projectId, slo.id, a)
    else if (editing) await sloApi.updateAlert(projectId, slo.id, editing.id, a)
    setEditing(null)
    onChanged()
  }

  const removeSlo = () => {
    if (window.confirm(`Delete the SLO "${slo.name}" with its burn alerts and counts?`)) run(sloApi.remove(projectId, slo.id), 'Could not delete the SLO')
  }
  const removeAlert = (a: BurnAlert) => {
    if (window.confirm(`Delete the ${formatWindow(a.windowMinutes)} burn alert?`)) run(sloApi.removeAlert(projectId, slo.id, a.id), 'Could not delete the burn alert')
  }

  const burns = new Map((status?.alerts ?? []).map((a) => [a.id, a]))
  return (
    <section aria-label={slo.name} style={cardStyle}>
      <div style={{ display: 'flex', justifyContent: 'space-between', gap: 8, flexWrap: 'wrap' }}>
        <div>
          <h2 style={{ fontSize: 16, margin: 0 }}>{slo.name}</h2>
          <div style={mutedText}>Target {pct(slo.target, 2)} over {slo.windowDays} days</div>
        </div>
        <div style={{ display: 'flex', gap: 6 }}>
          <button type="button" style={iconButton} onClick={onEdit} aria-label={`Edit ${slo.name}`}>Edit</button>
          <button type="button" style={iconButton} onClick={removeSlo} aria-label={`Delete ${slo.name}`}>Delete</button>
        </div>
      </div>
      {error && <div role="alert" style={{ ...errorStyle, marginBottom: 0 }}>{error}</div>}
      {status ? (
        <>
          <BudgetBar remaining={status.budgetRemaining} />
          <div style={mutedText}>
            {fmt(status.good)} good of {fmt(status.total)} total over the window
            {status.total > 0 && <> ({pct(status.good / status.total, 2)})</>}
          </div>
        </>
      ) : (
        <div style={mutedText}>Loading status...</div>
      )}

      <div>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <h3 style={{ fontSize: 13, margin: 0, color: '#d1d5db' }}>Burn alerts</h3>
          <button type="button" style={buttonStyle} onClick={() => setEditing('new')} aria-label={`Add burn alert to ${slo.name}`}>Add burn alert</button>
        </div>
        {alerts.length === 0 && editing !== 'new' && <p style={{ ...mutedText, margin: '6px 0 0' }}>No burn alerts on this SLO.</p>}
        <ul style={{ listStyle: 'none', padding: 0, margin: '6px 0 0', display: 'grid', gap: 6 }}>
          {alerts.map((a) => {
            const b = burns.get(a.id)
            return (
              <li key={a.id} style={{ display: 'flex', justifyContent: 'space-between', gap: 8, flexWrap: 'wrap', alignItems: 'center', fontSize: 13 }}>
                <span>
                  <strong>{formatWindow(a.windowMinutes)}</strong> window, fires at {a.burnRate}x
                  {b && <>, burning {b.currentBurn.toFixed(2)}x</>}
                  {!a.enabled && <span style={mutedText}> (disabled)</span>}
                  {(b?.firing ?? a.firing) && <span style={{ color: '#f87171', fontWeight: 700 }}> FIRING</span>}
                </span>
                <span style={{ display: 'flex', gap: 6 }}>
                  <button type="button" style={iconButton} onClick={() => setEditing(a)} aria-label={`Edit ${formatWindow(a.windowMinutes)} burn alert`}>Edit</button>
                  <button type="button" style={iconButton} onClick={() => removeAlert(a)} aria-label={`Delete ${formatWindow(a.windowMinutes)} burn alert`}>Delete</button>
                </span>
              </li>
            )
          })}
        </ul>
        {editing && (
          <BurnAlertForm
            key={editing === 'new' ? 'new' : editing.id}
            windowDays={slo.windowDays}
            alert={editing === 'new' ? null : editing}
            onSubmit={saveAlert}
            onCancel={() => setEditing(null)}
          />
        )}
      </div>
    </section>
  )
}
