import { useEffect, useState, type FormEvent, type ReactElement } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import type { Board } from '../api/types'
import { errorStyle, inputStyle, mutedText, primaryButton, selectStyle } from '../components/boards/styles'
import { BOARD_RANGES } from '../boards/model'

type Project = { id: number; name: string }

/** The boards of a project, and a form to start a new one. */
export function BoardsPage(): ReactElement {
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  const [projects, setProjects] = useState<Project[]>([])
  const [boards, setBoards] = useState<Board[] | null>(null)
  const [name, setName] = useState('')
  const [error, setError] = useState<string | null>(null)

  const projectId = Number(params.get('project')) || projects[0]?.id || 0

  useEffect(() => {
    api.listProjects().then((p) => setProjects((p ?? []).map(({ id, name }) => ({ id, name })))).catch(() => {})
  }, [])

  useEffect(() => {
    if (!projectId) return
    let cancelled = false
    api
      .listBoards(projectId)
      .then((b) => { if (!cancelled) setBoards(b ?? []) })
      .catch((e: unknown) => { if (!cancelled) setError(e instanceof Error ? e.message : 'Could not load boards') })
    return () => { cancelled = true }
  }, [projectId])

  const create = (e: FormEvent) => {
    e.preventDefault()
    const n = name.trim()
    if (!n || !projectId) return
    setError(null)
    api
      .createBoard(projectId, n)
      .then((r) => navigate(`/boards/${r.id}`))
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'Could not create the board'))
  }

  return (
    <div style={{ padding: 24 }}>
      <h1 style={{ fontSize: 20, fontWeight: 600, margin: '0 0 4px' }}>Boards</h1>
      <p style={{ ...mutedText, margin: '0 0 16px' }}>
        A board pins queries as charts and tables with one shared time range. Add panels with "Save to board" on the{' '}
        <Link to={`/analyze${projectId ? `?project=${projectId}` : ''}`} style={{ color: '#93c5fd' }}>query page</Link>.
      </p>

      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center', marginBottom: 16 }}>
        <select
          aria-label="Project"
          value={projectId}
          onChange={(e) => { setBoards(null); setParams({ project: e.target.value }) }}
          style={selectStyle}
        >
          {projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
        </select>
        <form onSubmit={create} style={{ display: 'flex', gap: 6 }}>
          <input aria-label="New board name" placeholder="New board name" value={name} maxLength={120} onChange={(e) => setName(e.target.value)} style={{ ...inputStyle, width: 220 }} />
          <button type="submit" style={primaryButton} disabled={!name.trim() || !projectId}>Create board</button>
        </form>
      </div>

      {error && <div role="alert" style={errorStyle}>{error}</div>}
      {boards === null ? (
        <p style={mutedText}>Loading boards...</p>
      ) : boards.length === 0 ? (
        <p style={mutedText}>No boards in this project yet.</p>
      ) : (
        <ul style={{ listStyle: 'none', padding: 0, margin: 0, display: 'grid', gap: 8, maxWidth: 640 }}>
          {boards.map((b) => (
            <li key={b.id}>
              <Link
                to={`/boards/${b.id}`}
                style={{ display: 'flex', justifyContent: 'space-between', gap: 12, padding: '10px 14px', background: '#111827', border: '1px solid #1f2937', borderRadius: 8, color: '#e5e7eb', textDecoration: 'none' }}
              >
                <span style={{ fontWeight: 600 }}>{b.name}</span>
                <span style={mutedText}>{BOARD_RANGES.find((r) => r.value === b.timeRange)?.label ?? b.timeRange}</span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
