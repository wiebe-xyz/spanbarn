import { useEffect, useState, type ReactElement } from 'react'
import { api } from '../../api/client'
import { ServiceSelect } from '../ServiceSelect'

export type DashboardFilterValues = {
  projectId: number
  service: string
  name: string
  status: string
}

type DashboardFiltersProps = {
  values: DashboardFilterValues
  onChange: (next: DashboardFilterValues) => void
}

type Project = { id: number; name: string }

const fieldStyle = { padding: '0.25rem 0.5rem', fontSize: '0.8125rem' }

/** The "Filter by" bar: project, service, span name and span status. */
export function DashboardFilters({ values, onChange }: DashboardFiltersProps): ReactElement {
  const [projects, setProjects] = useState<Project[]>([])
  const [name, setName] = useState(values.name)

  useEffect(() => {
    api.listProjects().then((p) => setProjects((p ?? []).map(({ id, name: n }) => ({ id, name: n })))).catch(() => {})
  }, [])

  // Keep the text box in step when the URL changes underneath it (back button).
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- mirror an external value into the draft
    setName(values.name)
  }, [values.name])

  const commitName = () => {
    if (name !== values.name) onChange({ ...values, name })
  }

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap' }}>
      <span style={{ fontSize: '0.8125rem', fontWeight: 600 }}>Filter by</span>
      <select
        aria-label="Project"
        value={values.projectId}
        onChange={(e) => onChange({ ...values, projectId: Number(e.target.value) })}
        style={fieldStyle}
      >
        <option value={0}>All projects</option>
        {projects.map((p) => (
          <option key={p.id} value={p.id}>{p.name}</option>
        ))}
      </select>
      <ServiceSelect value={values.service} onChange={(service) => onChange({ ...values, service })} range="24h" />
      <input
        aria-label="Span name"
        placeholder="Span name"
        value={name}
        onChange={(e) => setName(e.target.value)}
        onBlur={commitName}
        onKeyDown={(e) => {
          if (e.key === 'Enter') commitName()
        }}
        style={{ ...fieldStyle, width: 160 }}
      />
      <select
        aria-label="Span status"
        value={values.status}
        onChange={(e) => onChange({ ...values, status: e.target.value })}
        style={fieldStyle}
      >
        <option value="">Any status</option>
        <option value="ok">ok</option>
        <option value="error">error</option>
        <option value="unset">unset</option>
      </select>
    </div>
  )
}
