import { useCallback, useEffect, useState, type ReactElement } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import { sloApi, type BurnAlert, type Slo, type SloSettings, type SloStatus } from '../api/slos'
import { AutoRefresh } from '../components/AutoRefresh'
import { errorStyle, mutedText, primaryButton, selectStyle } from '../components/boards/styles'
import { SloCard } from '../components/slo/SloCard'
import { SloForm } from '../components/slo/SloForm'

type Project = { id: number; name: string }
type Row = { slo: Slo; status: SloStatus | null; alerts: BurnAlert[] }

async function loadRows(projectId: number): Promise<Row[]> {
  const slos = (await sloApi.list(projectId)) ?? []
  return Promise.all(
    slos.map(async (slo) => {
      const [status, alerts] = await Promise.all([
        sloApi.status(projectId, slo.id).catch(() => null),
        sloApi.listAlerts(projectId, slo.id).catch(() => [] as BurnAlert[]),
      ])
      return { slo, status, alerts: alerts ?? [] }
    }),
  )
}

/** The SLOs of a project with their error budget, burn and burn alerts. */
export function SlosPage(): ReactElement {
  const [params, setParams] = useSearchParams()
  const [projects, setProjects] = useState<Project[]>([])
  const [rows, setRows] = useState<Row[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [form, setForm] = useState<Slo | 'new' | null>(null)
  const [refreshSeconds, setRefreshSeconds] = useState(30)

  const projectId = Number(params.get('project')) || projects[0]?.id || 0

  useEffect(() => {
    api.listProjects().then((p) => setProjects((p ?? []).map(({ id, name }) => ({ id, name })))).catch(() => {})
  }, [])

  const refresh = useCallback(() => {
    if (!projectId) return
    loadRows(projectId)
      .then((r) => { setRows(r); setError(null) })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : 'Could not load the SLOs'))
  }, [projectId])

  useEffect(() => { refresh() }, [refresh])

  const save = async (s: SloSettings) => {
    if (form === 'new') await sloApi.create(projectId, s)
    else if (form) await sloApi.update(projectId, form.id, s)
    setForm(null)
    refresh()
  }

  return (
    <div style={{ padding: 24 }}>
      <h1 style={{ fontSize: 20, fontWeight: 600, margin: '0 0 4px' }}>SLOs</h1>
      <p style={{ ...mutedText, margin: '0 0 16px' }}>
        A service level objective holds the share of good spans to a target over a rolling window. Burn alerts fire when the error budget burns faster than a set rate.
      </p>

      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center', marginBottom: 16 }}>
        <select
          aria-label="Project"
          value={projectId}
          onChange={(e) => { setRows(null); setForm(null); setParams({ project: e.target.value }) }}
          style={selectStyle}
        >
          {projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
        </select>
        <button type="button" style={primaryButton} disabled={!projectId} onClick={() => setForm('new')}>New SLO</button>
        <AutoRefresh value={refreshSeconds} onChange={setRefreshSeconds} onRefresh={refresh} />
      </div>

      {form && (
        <SloForm
          key={form === 'new' ? 'new' : form.id}
          projectId={projectId}
          slo={form === 'new' ? null : form}
          onSubmit={save}
          onCancel={() => setForm(null)}
        />
      )}

      {error && <div role="alert" style={errorStyle}>{error}</div>}
      {rows === null ? (
        error ? null : <p style={mutedText}>Loading SLOs...</p>
      ) : rows.length === 0 ? (
        <p style={mutedText}>No SLOs in this project yet.</p>
      ) : (
        <div style={{ display: 'grid', gap: 12, maxWidth: 720 }}>
          {rows.map((r) => (
            <SloCard key={r.slo.id} projectId={projectId} slo={r.slo} status={r.status} alerts={r.alerts} onEdit={() => setForm(r.slo)} onChanged={refresh} />
          ))}
        </div>
      )}
    </div>
  )
}
