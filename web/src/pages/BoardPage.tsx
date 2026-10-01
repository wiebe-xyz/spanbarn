import { useCallback, useEffect, useMemo, useState, type FormEvent, type ReactElement } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api } from '../api/client'
import type { Board, PanelView, Release } from '../api/types'
import { PanelCard } from '../components/boards/PanelCard'
import { buttonStyle, errorStyle, inputStyle, mutedText, selectStyle } from '../components/boards/styles'
import { BOARD_RANGES, REFRESH_OPTIONS, markersFrom, windowFor, type Window } from '../boards/model'

/** A board: its panels in order, one shared time range, a refresh interval and release markers. */
export function BoardPage(): ReactElement {
  const { id } = useParams()
  const boardId = Number(id)
  const navigate = useNavigate()
  const [board, setBoard] = useState<Board | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [tick, setTick] = useState(0)
  const [win, setWin] = useState<Window>(() => windowFor('24h', Date.now()))
  const [releases, setReleases] = useState<Release[]>([])
  const [version, setVersion] = useState('')

  const fail = (e: unknown) => setError(e instanceof Error ? e.message : 'Request failed')

  const load = useCallback(() => {
    return api
      .getBoard(boardId)
      .then((b) => {
        setBoard(b)
        setWin(windowFor(b.timeRange, Date.now()))
      })
      .catch(fail)
  }, [boardId])

  useEffect(() => {
    load()
  }, [load])

  const range = board?.timeRange
  const refreshSeconds = board?.refreshSeconds ?? 0
  const projectId = board?.projectId ?? 0

  // Every refresh moves the window to now and reloads the panels.
  const refresh = useCallback(() => {
    setWin(windowFor(range ?? '24h', Date.now()))
    setTick((n) => n + 1)
  }, [range])

  useEffect(() => {
    if (refreshSeconds <= 0) return
    const timer = setInterval(refresh, refreshSeconds * 1000)
    return () => clearInterval(timer)
  }, [refreshSeconds, refresh])

  useEffect(() => {
    if (!projectId) return
    let cancelled = false
    api
      .listReleases(projectId, new Date(win.fromMs).toISOString(), new Date(win.toMs).toISOString())
      .then((r) => { if (!cancelled) setReleases(r ?? []) })
      .catch(() => { if (!cancelled) setReleases([]) })
    return () => { cancelled = true }
  }, [projectId, win.fromMs, win.toMs])

  const markers = useMemo(() => markersFrom(releases, win), [releases, win])

  const settings = (changes: Partial<{ name: string; timeRange: string; refreshSeconds: number }>) => {
    if (!board) return
    const next = { name: board.name, timeRange: board.timeRange, refreshSeconds: board.refreshSeconds, ...changes }
    setBoard({ ...board, ...next })
    if (changes.timeRange) {
      setWin(windowFor(changes.timeRange, Date.now()))
      setTick((n) => n + 1)
    }
    api.updateBoard(board.id, next).catch((e: unknown) => { fail(e); load() })
  }

  const move = (index: number, dir: -1 | 1) => {
    if (!board) return
    const ids = board.panels.map((p) => p.id)
    const to = index + dir
    if (to < 0 || to >= ids.length) return
    ;[ids[index], ids[to]] = [ids[to], ids[index]]
    setBoard({ ...board, panels: ids.map((pid) => board.panels.find((p) => p.id === pid)!) })
    api.reorderPanels(board.id, ids).catch((e: unknown) => { fail(e); load() })
  }

  const setView = (panelId: number, title: string, view: PanelView) => {
    if (!board) return
    setBoard({ ...board, panels: board.panels.map((p) => (p.id === panelId ? { ...p, view } : p)) })
    api.updatePanel(board.id, panelId, title, view).catch((e: unknown) => { fail(e); load() })
  }

  const rename = (panelId: number, title: string, view: PanelView) => {
    if (!board) return
    setBoard({ ...board, panels: board.panels.map((p) => (p.id === panelId ? { ...p, title } : p)) })
    api.updatePanel(board.id, panelId, title, view).catch((e: unknown) => { fail(e); load() })
  }

  const remove = (panelId: number) => {
    if (!board) return
    api.deletePanel(board.id, panelId).then(load).catch(fail)
  }

  const removeBoard = () => {
    if (!board || !window.confirm(`Delete board "${board.name}" and its panels?`)) return
    api.deleteBoard(board.id).then(() => navigate(`/boards?project=${board.projectId}`)).catch(fail)
  }

  const markRelease = (e: FormEvent) => {
    e.preventDefault()
    const v = version.trim()
    if (!v || !board) return
    api
      .createRelease(board.projectId, v)
      .then(() => {
        setVersion('')
        refresh()
      })
      .catch(fail)
  }

  if (!board) {
    return (
      <div style={{ padding: 24 }}>
        {error ? <div role="alert" style={errorStyle}>{error}</div> : <p style={mutedText}>Loading board...</p>}
        <Link to="/boards" style={{ color: '#93c5fd', fontSize: 13 }}>All boards</Link>
      </div>
    )
  }

  return (
    <div style={{ padding: 24 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap', marginBottom: 4 }}>
        <h1 style={{ fontSize: 20, fontWeight: 600, margin: 0 }}>{board.name}</h1>
        <Link to={`/boards?project=${board.projectId}`} style={{ fontSize: 13, color: '#93c5fd' }}>All boards</Link>
      </div>
      <p style={{ ...mutedText, margin: '0 0 12px' }}>
        Every panel uses this time range and refreshes together. Dashed lines mark releases.
      </p>

      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center', marginBottom: 16 }}>
        <select aria-label="Time range" value={board.timeRange} onChange={(e) => settings({ timeRange: e.target.value })} style={selectStyle}>
          {BOARD_RANGES.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
        </select>
        <select aria-label="Refresh interval" value={board.refreshSeconds} onChange={(e) => settings({ refreshSeconds: Number(e.target.value) })} style={selectStyle}>
          {REFRESH_OPTIONS.map((o) => <option key={o.seconds} value={o.seconds}>{o.label}</option>)}
        </select>
        <button type="button" style={buttonStyle} onClick={refresh}>Refresh now</button>
        <form onSubmit={markRelease} style={{ display: 'flex', gap: 6 }}>
          <input aria-label="Release version" placeholder="Release version" value={version} maxLength={120} onChange={(e) => setVersion(e.target.value)} style={{ ...inputStyle, width: 150 }} />
          <button type="submit" style={buttonStyle} disabled={!version.trim()}>Mark release</button>
        </form>
        <span style={{ flex: 1 }} />
        <button type="button" style={buttonStyle} onClick={removeBoard}>Delete board</button>
      </div>

      {error && <div role="alert" style={errorStyle}>{error}</div>}

      {board.panels.length === 0 ? (
        <p style={mutedText}>
          This board has no panels. Run a query and choose "Save to board" to add one.{' '}
          <Link to={`/analyze?project=${board.projectId}`} style={{ color: '#93c5fd' }}>Open the query page</Link>
        </p>
      ) : (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 460px), 1fr))', gap: 16 }}>
          {board.panels.map((panel, i) => (
            <PanelCard
              key={panel.id}
              board={board}
              panel={panel}
              win={win}
              tick={tick}
              markers={markers}
              first={i === 0}
              last={i === board.panels.length - 1}
              onMove={(dir) => move(i, dir)}
              onRemove={() => remove(panel.id)}
              onView={(view) => setView(panel.id, panel.title, view)}
              onRename={(title) => rename(panel.id, title, panel.view)}
            />
          ))}
        </div>
      )}
    </div>
  )
}
