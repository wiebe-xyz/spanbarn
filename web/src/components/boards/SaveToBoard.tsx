import { useEffect, useState, type FormEvent, type ReactElement } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../../api/client'
import type { Board } from '../../api/types'
import { definitionFromState, filtersFromState } from '../../boards/model'
import { calcLabel, type QueryState } from '../../analyze/model'
import { buttonStyle, errorStyle, inputStyle, mutedText, primaryButton, selectStyle } from './styles'

const NEW_BOARD = 'new'

type Props = {
  projectId: number
  /** The query that produced the result on screen. */
  state: QueryState
}

function defaultTitle(s: QueryState): string {
  const calc = calcLabel(s.calcs[0] ?? 'count')
  return s.groupBy.length > 0 ? `${calc} by ${s.groupBy.join(', ')}` : `${calc} of all spans`
}

/** Adds the query on screen to a board as a panel, on an existing board or a new one. */
export function SaveToBoard({ projectId, state }: Props): ReactElement {
  const [open, setOpen] = useState(false)
  const [boards, setBoards] = useState<Board[]>([])
  const [target, setTarget] = useState(NEW_BOARD)
  const [boardName, setBoardName] = useState('')
  const [title, setTitle] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState<{ id: number; name: string } | null>(null)

  useEffect(() => {
    if (!open || !projectId) return
    let cancelled = false
    api
      .listBoards(projectId)
      .then((b) => {
        if (cancelled) return
        setBoards(b ?? [])
        setTarget((t) => (t === NEW_BOARD && (b ?? []).length > 0 ? String(b[0].id) : t))
      })
      .catch(() => { if (!cancelled) setBoards([]) })
    return () => { cancelled = true }
  }, [open, projectId])

  const toggle = () => {
    if (!open) {
      setTitle(defaultTitle(state))
      setSaved(null)
      setError(null)
    }
    setOpen(!open)
  }

  const save = async (e: FormEvent) => {
    e.preventDefault()
    setSaving(true)
    setError(null)
    try {
      let id = Number(target)
      let name = boards.find((b) => b.id === id)?.name ?? ''
      if (target === NEW_BOARD) {
        name = boardName.trim()
        id = (await api.createBoard(projectId, name, state.range)).id
      }
      await api.addPanel(id, {
        title: title.trim(),
        view: state.view,
        filters: filtersFromState(state),
        definition: definitionFromState(state),
      })
      setSaved({ id, name })
      setOpen(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not save the panel')
    } finally {
      setSaving(false)
    }
  }

  const needsName = target === NEW_BOARD && !boardName.trim()

  return (
    <div style={{ margin: '12px 0' }}>
      <button type="button" style={buttonStyle} aria-expanded={open} onClick={toggle}>Save to board</button>
      {saved && (
        <span role="status" style={{ ...mutedText, marginLeft: 12 }}>
          Saved to <Link to={`/boards/${saved.id}`} style={{ color: '#93c5fd' }}>{saved.name}</Link>.
        </span>
      )}
      {open && (
        <form onSubmit={save} aria-label="Save to board" style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center', marginTop: 8 }}>
          <select aria-label="Board" value={target} onChange={(e) => setTarget(e.target.value)} style={selectStyle}>
            {boards.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
            <option value={NEW_BOARD}>New board...</option>
          </select>
          {target === NEW_BOARD && (
            <input aria-label="Board name" placeholder="Board name" value={boardName} maxLength={120} onChange={(e) => setBoardName(e.target.value)} style={{ ...inputStyle, width: 180 }} />
          )}
          <input aria-label="Panel title" placeholder="Panel title" value={title} maxLength={200} onChange={(e) => setTitle(e.target.value)} style={{ ...inputStyle, width: 240 }} />
          <button type="submit" style={primaryButton} disabled={saving || needsName}>Add panel</button>
        </form>
      )}
      {error && <div role="alert" style={{ ...errorStyle, marginTop: 8 }}>{error}</div>}
    </div>
  )
}
