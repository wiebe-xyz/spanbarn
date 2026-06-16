import { useState, useEffect, useCallback, type ReactElement, type FormEvent } from 'react'
import { Bell, Plus, Trash2, Pencil, X, Check } from 'lucide-react'
import { api } from '../api/client'
import type { Alert } from '../api/types'
import { ServiceSelect } from '../components/ServiceSelect'
import { useTimeRange } from '../contexts/useTimeRange'

type AlertType = 'latency' | 'error_rate' | 'metric_threshold'

type AlertFormState = {
  service: string
  operation: string
  type: AlertType
  threshold: string
  comparisonWindow: string
  cooldownMinutes: string
  webhookUrl: string
  email: string
  enabled: boolean
  metricName: string
  metricAgg: string
  labelFilters: string // "key=value, key2=value2"
}

const emptyForm = (): AlertFormState => ({
  service: '',
  operation: '',
  type: 'latency',
  threshold: '',
  comparisonWindow: '10',
  cooldownMinutes: '30',
  webhookUrl: '',
  email: '',
  enabled: true,
  metricName: '',
  metricAgg: 'last',
  labelFilters: '',
})

// formatLabelFilters renders a label map as "k=v, k2=v2" for the text input.
function formatLabelFilters(m?: Record<string, string>): string {
  if (!m) return ''
  return Object.entries(m).map(([k, v]) => `${k}=${v}`).join(', ')
}

// parseLabelFilters parses "k=v, k2=v2" back into a label map.
function parseLabelFilters(s: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const part of s.split(',')) {
    const [k, ...rest] = part.split('=')
    const key = k.trim()
    if (key && rest.length > 0) out[key] = rest.join('=').trim()
  }
  return out
}

function alertToForm(a: Alert): AlertFormState {
  return {
    service: a.service,
    operation: a.operation,
    type: a.type,
    threshold: String(a.threshold),
    comparisonWindow: String(a.comparisonWindow),
    cooldownMinutes: String(a.cooldownMinutes),
    webhookUrl: a.webhookUrl,
    email: a.email,
    enabled: a.enabled,
    metricName: a.metricName ?? '',
    metricAgg: a.metricAgg || 'last',
    labelFilters: formatLabelFilters(a.labelFilters),
  }
}

function thresholdLabel(type: AlertType) {
  if (type === 'latency') return 'Threshold (ms)'
  if (type === 'error_rate') return 'Threshold (%)'
  return 'Threshold'
}

function thresholdDisplay(a: Alert) {
  if (a.type === 'latency') return `> ${a.threshold} ms`
  if (a.type === 'error_rate') return `> ${a.threshold}%`
  return `${a.metricAgg ?? ''} > ${a.threshold}`
}

function relativeTime(iso: string) {
  const diff = Date.now() - new Date(iso).getTime()
  const m = Math.floor(diff / 60000)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
}

export function AlertsPage(): ReactElement {
  const { range } = useTimeRange()
  const [alerts, setAlerts] = useState<Alert[]>([])
  const [loading, setLoading] = useState(true)
  const [showForm, setShowForm] = useState(false)
  const [editId, setEditId] = useState<number | null>(null)
  const [form, setForm] = useState<AlertFormState>(emptyForm())
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [defaultProjectId, setDefaultProjectId] = useState(0)

  const load = useCallback(async () => {
    try {
      const [data, projects] = await Promise.all([
        api.listAlerts(),
        api.listProjects().catch(() => [] as { id: number; slug: string; name: string; status: string }[]),
      ])
      setAlerts(data ?? [])
      const active = (projects ?? []).find(p => p.status === 'active') ?? (projects ?? [])[0]
      if (active) setDefaultProjectId(active.id)
    } catch {
      // handled by client
    } finally {
      setLoading(false)
    }
  }, [])

  // Initial fetch on mount. The cascading-renders warning is a false positive
  // here: `load` is a stable useCallback (empty deps) and the setState calls
  // run after an async network round-trip, not synchronously inside the effect.
  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { void load() }, [load])

  const openCreate = () => {
    setEditId(null)
    setForm(emptyForm())
    setError('')
    setShowForm(true)
  }

  const openEdit = (a: Alert) => {
    setEditId(a.id)
    setForm(alertToForm(a))
    setError('')
    setShowForm(true)
  }

  const closeForm = () => {
    setShowForm(false)
    setEditId(null)
    setError('')
  }

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    const isMetric = form.type === 'metric_threshold'
    if (isMetric) {
      if (!form.metricName) { setError('Metric name is required'); return }
    } else if (!form.service) {
      setError('Service is required'); return
    }
    if (!form.threshold) { setError('Threshold is required'); return }
    if (!defaultProjectId) { setError('Projects still loading — please wait a moment and try again'); return }

    const payload = {
      projectId: defaultProjectId,
      service: form.service,
      operation: form.operation,
      type: form.type,
      threshold: parseFloat(form.threshold),
      comparisonWindow: parseInt(form.comparisonWindow) || 10,
      cooldownMinutes: parseInt(form.cooldownMinutes) || 30,
      webhookUrl: form.webhookUrl,
      email: form.email,
      enabled: form.enabled,
      metricName: isMetric ? form.metricName : '',
      metricAgg: isMetric ? form.metricAgg : '',
      labelFilters: isMetric ? parseLabelFilters(form.labelFilters) : {},
    }

    setSaving(true)
    try {
      if (editId !== null) {
        await api.updateAlert(editId, payload)
      } else {
        await api.createAlert(payload)
      }
      await load()
      closeForm()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save alert')
    } finally {
      setSaving(false)
    }
  }

  const handleDelete = async (id: number) => {
    if (!confirm('Delete this alert?')) return
    try {
      await api.deleteAlert(id)
      setAlerts((prev) => prev.filter((a) => a.id !== id))
    } catch {
      // handled by client
    }
  }

  const handleToggleEnabled = async (a: Alert) => {
    try {
      await api.updateAlert(a.id, { ...a, enabled: !a.enabled })
      setAlerts((prev) => prev.map((x) => x.id === a.id ? { ...x, enabled: !x.enabled } : x))
    } catch {
      // handled by client
    }
  }

  const typeBadge = (type: AlertType) => {
    if (type === 'latency') return { bg: 'rgba(99,102,241,0.15)', color: '#818cf8', label: 'Latency' }
    if (type === 'error_rate') return { bg: 'rgba(239,68,68,0.15)', color: '#f87171', label: 'Error rate' }
    return { bg: 'rgba(34,197,94,0.15)', color: '#4ade80', label: 'Metric' }
  }

  const inputStyle: React.CSSProperties = {
    width: '100%',
    padding: '0.5rem 0.75rem',
    background: 'var(--surface)',
    border: '1px solid var(--border)',
    borderRadius: '0.5rem',
    color: 'var(--text)',
    fontSize: '0.875rem',
    boxSizing: 'border-box',
  }

  const labelStyle: React.CSSProperties = {
    display: 'block',
    fontSize: '0.75rem',
    color: 'var(--text-muted)',
    marginBottom: '0.25rem',
    fontWeight: 500,
  }

  return (
    <div>
      {/* Header */}
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginBottom: '1.5rem',
          flexWrap: 'wrap',
          gap: '0.75rem',
        }}
      >
        <h2 style={{ fontSize: '1.25rem', fontWeight: 700 }}>Alerts</h2>
        <button className="btn btn-primary" onClick={openCreate}>
          <Plus size={15} />
          New alert
        </button>
      </div>

      {/* Alert list */}
      {loading ? (
        <div className="card">
          {Array.from({ length: 3 }).map((_, i) => (
            <div key={i} style={{ display: 'flex', gap: '1rem', padding: '1rem 0', borderBottom: i < 2 ? '1px solid var(--border)' : 'none' }}>
              <div className="skeleton" style={{ width: 120, height: 18 }} />
              <div className="skeleton" style={{ width: 80, height: 18 }} />
              <div className="skeleton" style={{ flex: 1, height: 18 }} />
            </div>
          ))}
        </div>
      ) : alerts.length === 0 ? (
        <div
          className="card"
          style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}
        >
          <Bell size={32} style={{ marginBottom: '0.75rem', opacity: 0.4 }} />
          <p style={{ marginBottom: '0.5rem' }}>No alerts configured</p>
          <p style={{ fontSize: '0.875rem' }}>
            Create an alert to be notified when latency or error rate exceeds a threshold.
          </p>
        </div>
      ) : (
        <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
          <div style={{ overflowX: 'auto' }}>
            <table>
              <thead>
                <tr>
                  <th style={{ textAlign: 'left' }}>Service / Operation</th>
                  <th style={{ textAlign: 'left' }}>Type</th>
                  <th style={{ textAlign: 'left' }}>Condition</th>
                  <th style={{ textAlign: 'left' }}>Notify</th>
                  <th style={{ textAlign: 'left' }}>Last triggered</th>
                  <th style={{ textAlign: 'left' }}>Status</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {alerts.map((a) => {
                  const badge = typeBadge(a.type)
                  return (
                    <tr key={a.id}>
                      <td>
                        {a.type === 'metric_threshold' ? (
                          <span style={{ fontWeight: 600, fontFamily: 'monospace', fontSize: '0.8125rem' }}>{a.metricName}</span>
                        ) : (
                          <>
                            <span style={{ fontWeight: 600 }}>{a.service}</span>
                            {a.operation && (
                              <span style={{ color: 'var(--text-muted)', fontSize: '0.8125rem' }}>
                                {' '}/ {a.operation}
                              </span>
                            )}
                          </>
                        )}
                      </td>
                      <td>
                        <span
                          style={{
                            display: 'inline-block',
                            padding: '0.125rem 0.5rem',
                            borderRadius: '1rem',
                            fontSize: '0.6875rem',
                            fontWeight: 700,
                            background: badge.bg,
                            color: badge.color,
                          }}
                        >
                          {badge.label}
                        </span>
                      </td>
                      <td className="mono" style={{ fontSize: '0.8125rem' }}>
                        {thresholdDisplay(a)}
                        <span style={{ color: 'var(--text-muted)', fontSize: '0.75rem', marginLeft: '0.375rem' }}>
                          over {a.comparisonWindow}m
                        </span>
                      </td>
                      <td style={{ fontSize: '0.8125rem', color: 'var(--text-muted)' }}>
                        {a.webhookUrl && <span title={a.webhookUrl}>webhook</span>}
                        {a.webhookUrl && a.email && <span style={{ margin: '0 0.25rem' }}>·</span>}
                        {a.email && <span>{a.email}</span>}
                        {!a.webhookUrl && !a.email && <span style={{ opacity: 0.5 }}>—</span>}
                      </td>
                      <td style={{ fontSize: '0.8125rem', color: 'var(--text-muted)' }}>
                        {a.lastTriggeredAt ? relativeTime(a.lastTriggeredAt) : '—'}
                      </td>
                      <td>
                        <button
                          onClick={() => handleToggleEnabled(a)}
                          style={{
                            display: 'inline-flex',
                            alignItems: 'center',
                            gap: '0.25rem',
                            padding: '0.125rem 0.625rem',
                            borderRadius: '1rem',
                            fontSize: '0.6875rem',
                            fontWeight: 600,
                            border: 'none',
                            cursor: 'pointer',
                            background: a.enabled ? 'rgba(34,197,94,0.15)' : 'rgba(148,163,184,0.15)',
                            color: a.enabled ? '#22c55e' : '#94a3b8',
                          }}
                          title={a.enabled ? 'Click to disable' : 'Click to enable'}
                        >
                          {a.enabled ? <><Check size={10} /> Active</> : 'Paused'}
                        </button>
                      </td>
                      <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                        <button
                          className="btn"
                          style={{ padding: '0.25rem 0.5rem', marginRight: '0.25rem' }}
                          onClick={() => openEdit(a)}
                          title="Edit"
                        >
                          <Pencil size={13} />
                        </button>
                        <button
                          className="btn"
                          style={{ padding: '0.25rem 0.5rem', color: '#ef4444' }}
                          onClick={() => handleDelete(a.id)}
                          title="Delete"
                        >
                          <Trash2 size={13} />
                        </button>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {/* Create / edit form modal */}
      {showForm && (
        <div
          style={{
            position: 'fixed',
            inset: 0,
            background: 'rgba(0,0,0,0.6)',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            zIndex: 100,
            padding: '1rem',
          }}
          onClick={(e) => { if (e.target === e.currentTarget) closeForm() }}
        >
          <div
            className="card"
            style={{ width: '100%', maxWidth: 520, maxHeight: '90vh', overflowY: 'auto' }}
          >
            {/* Modal header */}
            <div
              style={{
                display: 'flex',
                justifyContent: 'space-between',
                alignItems: 'center',
                marginBottom: '1.25rem',
              }}
            >
              <h3 style={{ fontSize: '1rem', fontWeight: 700 }}>
                {editId !== null ? 'Edit alert' : 'New alert'}
              </h3>
              <button className="btn" style={{ padding: '0.25rem' }} onClick={closeForm}>
                <X size={16} />
              </button>
            </div>

            <form onSubmit={(e) => { void handleSubmit(e) }}>
              <div style={{ display: 'grid', gap: '1rem' }}>
                {/* Alert type selector first — it switches the rest of the form. */}
                <div>
                  <label style={labelStyle}>Alert type *</label>
                  <select
                    style={{ ...inputStyle, appearance: 'auto' }}
                    value={form.type}
                    onChange={(e) => setForm((f) => ({ ...f, type: e.target.value as AlertType }))}
                  >
                    <option value="latency">Latency (p99)</option>
                    <option value="error_rate">Error rate</option>
                    <option value="metric_threshold">Metric threshold</option>
                  </select>
                </div>

                {form.type === 'metric_threshold' ? (
                  <>
                    {/* Metric name + aggregation */}
                    <div style={{ display: 'grid', gridTemplateColumns: '2fr 1fr', gap: '0.75rem' }}>
                      <div>
                        <label style={labelStyle}>Metric name *</label>
                        <input
                          style={inputStyle}
                          placeholder="e.g. http.server.active_requests"
                          value={form.metricName}
                          onChange={(e) => setForm((f) => ({ ...f, metricName: e.target.value }))}
                        />
                      </div>
                      <div>
                        <label style={labelStyle}>Aggregation</label>
                        <select
                          style={{ ...inputStyle, appearance: 'auto' }}
                          value={form.metricAgg}
                          onChange={(e) => setForm((f) => ({ ...f, metricAgg: e.target.value }))}
                        >
                          <option value="last">Last value</option>
                          <option value="avg">Average</option>
                          <option value="rate">Rate /s (counters)</option>
                          <option value="p95">p95 (histograms)</option>
                        </select>
                      </div>
                    </div>
                    {/* Label filters */}
                    <div>
                      <label style={labelStyle}>Label filters (optional)</label>
                      <input
                        style={inputStyle}
                        placeholder="e.g. service.name=api, route=/checkout"
                        value={form.labelFilters}
                        onChange={(e) => setForm((f) => ({ ...f, labelFilters: e.target.value }))}
                      />
                    </div>
                  </>
                ) : (
                  <>
                    {/* Service */}
                    <div>
                      <label style={labelStyle}>Service *</label>
                      <ServiceSelect
                        value={form.service}
                        onChange={(v) => setForm((f) => ({ ...f, service: v }))}
                        range={range}
                      />
                    </div>

                    {/* Operation */}
                    <div>
                      <label style={labelStyle}>Operation (optional)</label>
                      <input
                        style={inputStyle}
                        placeholder="e.g. GET /api/users — leave blank for all operations"
                        value={form.operation}
                        onChange={(e) => setForm((f) => ({ ...f, operation: e.target.value }))}
                      />
                    </div>
                  </>
                )}

                {/* Threshold */}
                <div>
                  <label style={labelStyle}>{thresholdLabel(form.type)} *</label>
                  <input
                    style={inputStyle}
                    type="number"
                    min="0"
                    step={form.type === 'error_rate' ? '0.1' : '1'}
                    placeholder={form.type === 'latency' ? '500' : form.type === 'error_rate' ? '5' : '100'}
                    value={form.threshold}
                    onChange={(e) => setForm((f) => ({ ...f, threshold: e.target.value }))}
                    required
                  />
                </div>

                {/* Windows */}
                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.75rem' }}>
                  <div>
                    <label style={labelStyle}>Comparison window (min)</label>
                    <input
                      style={inputStyle}
                      type="number"
                      min="1"
                      value={form.comparisonWindow}
                      onChange={(e) => setForm((f) => ({ ...f, comparisonWindow: e.target.value }))}
                    />
                  </div>
                  <div>
                    <label style={labelStyle}>Cooldown (min)</label>
                    <input
                      style={inputStyle}
                      type="number"
                      min="1"
                      value={form.cooldownMinutes}
                      onChange={(e) => setForm((f) => ({ ...f, cooldownMinutes: e.target.value }))}
                    />
                  </div>
                </div>

                {/* Notifications */}
                <div>
                  <label style={labelStyle}>Webhook URL</label>
                  <input
                    style={inputStyle}
                    type="url"
                    placeholder="https://hooks.example.com/..."
                    value={form.webhookUrl}
                    onChange={(e) => setForm((f) => ({ ...f, webhookUrl: e.target.value }))}
                  />
                </div>
                <div>
                  <label style={labelStyle}>Email</label>
                  <input
                    style={inputStyle}
                    type="email"
                    placeholder="ops@example.com"
                    value={form.email}
                    onChange={(e) => setForm((f) => ({ ...f, email: e.target.value }))}
                  />
                </div>

                {/* Enabled toggle */}
                <label
                  style={{ display: 'flex', alignItems: 'center', gap: '0.625rem', cursor: 'pointer', fontSize: '0.875rem' }}
                >
                  <input
                    type="checkbox"
                    checked={form.enabled}
                    onChange={(e) => setForm((f) => ({ ...f, enabled: e.target.checked }))}
                  />
                  Enable alert immediately
                </label>
              </div>

              {error && (
                <p style={{ color: '#f87171', fontSize: '0.875rem', marginTop: '0.75rem' }}>{error}</p>
              )}

              <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem', marginTop: '1.25rem' }}>
                <button type="button" className="btn" onClick={closeForm}>Cancel</button>
                <button type="submit" className="btn btn-primary" disabled={saving}>
                  {saving ? 'Saving…' : editId !== null ? 'Save changes' : 'Create alert'}
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  )
}
