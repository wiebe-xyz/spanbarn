import { type CSSProperties, type KeyboardEvent, type ReactElement, useEffect, useMemo, useRef, useState } from 'react'
import { arrangeProjects, firstMatch, type MenuProject } from '../utils/projectMenu'

const FAVOURITES_KEY = 'spanbarn.favourite-projects'

function readFavourites(): Set<number> {
  try {
    const raw = localStorage.getItem(FAVOURITES_KEY)
    const ids: unknown = raw ? JSON.parse(raw) : []
    return new Set(Array.isArray(ids) ? ids.filter((id): id is number => typeof id === 'number') : [])
  } catch {
    return new Set()
  }
}

function writeFavourites(ids: Set<number>): void {
  try {
    localStorage.setItem(FAVOURITES_KEY, JSON.stringify([...ids]))
  } catch {
    // Favourites stay for this page view when storage is unavailable.
  }
}

type Props = {
  projects: readonly MenuProject[]
  /** The selected project id as a string, '' or '0' for none. */
  value: string
  onChange: (id: string) => void
  /** When set, a first row with this label selects '0' (all projects). */
  allLabel?: string
  style?: CSSProperties
}

const row: CSSProperties = { display: 'flex', alignItems: 'center', gap: 4 }
const pick: CSSProperties = {
  flex: 1, textAlign: 'left', padding: '0.375rem 0.5rem', background: 'transparent',
  color: 'inherit', border: 'none', cursor: 'pointer', font: 'inherit',
}
const star: CSSProperties = { padding: '0.25rem 0.5rem', background: 'transparent', border: 'none', cursor: 'pointer', font: 'inherit' }
const heading: CSSProperties = { padding: '0.375rem 0.5rem 0.125rem', fontSize: '0.75rem', opacity: 0.7 }

/**
 * Project picker with search, favourites, an internally scrolling list and an
 * alphabetical order. Favourites live in this browser: SpanBarn has no per-user
 * settings store yet.
 */
export function ProjectSwitcher({ projects, value, onChange, allLabel, style }: Props): ReactElement {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [favourites, setFavourites] = useState<Set<number>>(readFavourites)
  const trigger = useRef<HTMLButtonElement>(null)
  const search = useRef<HTMLInputElement>(null)
  const root = useRef<HTMLDivElement>(null)

  const menu = useMemo(() => arrangeProjects(projects, query, favourites), [projects, query, favourites])
  const current = projects.find((p) => String(p.id) === value)
  const showAll = allLabel !== undefined && allLabel.toLowerCase().includes(query.trim().toLowerCase())
  const empty = !showAll && menu.favourites.length === 0 && menu.others.length === 0

  const close = (restoreFocus: boolean) => {
    setOpen(false)
    setQuery('')
    if (restoreFocus) trigger.current?.focus()
  }

  useEffect(() => {
    if (!open) return
    search.current?.focus()
    const previous = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    const away = (e: MouseEvent) => {
      if (root.current && !root.current.contains(e.target as Node)) close(false)
    }
    document.addEventListener('mousedown', away)
    return () => {
      document.body.style.overflow = previous
      document.removeEventListener('mousedown', away)
    }
  }, [open])

  useEffect(() => {
    if (open) root.current?.querySelector('[aria-current="true"]')?.scrollIntoView?.({ block: 'nearest' })
  }, [open])

  const choose = (id: string) => {
    onChange(id)
    close(true)
  }

  const toggleFavourite = (id: number) => {
    const next = new Set(favourites)
    if (!next.delete(id)) next.add(id)
    setFavourites(next)
    writeFavourites(next)
  }

  const onSearchKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      if (showAll && !query.trim()) return choose('0')
      const first = firstMatch(menu)
      if (first) choose(String(first.id))
    }
  }

  const rowFor = (p: MenuProject) => (
    <div key={p.id} style={row}>
      <button type="button" style={pick} aria-current={String(p.id) === value ? 'true' : undefined} onClick={() => choose(String(p.id))}>
        {String(p.id) === value ? '✓ ' : ''}{p.name}
      </button>
      <button
        type="button"
        style={star}
        aria-pressed={favourites.has(p.id)}
        aria-label={`Favourite ${p.name}`}
        onClick={() => toggleFavourite(p.id)}
      >
        {favourites.has(p.id) ? '★' : '☆'}
      </button>
    </div>
  )

  return (
    <div
      ref={root}
      style={{ position: 'relative', display: 'inline-block' }}
      onKeyDown={(e) => {
        if (e.key === 'Escape' && open) {
          e.stopPropagation()
          close(true)
        }
      }}
    >
      <button
        ref={trigger}
        type="button"
        aria-label="Project"
        aria-haspopup="true"
        aria-expanded={open}
        onClick={() => (open ? close(false) : setOpen(true))}
        style={{ ...style, minWidth: 140, textAlign: 'left', cursor: 'pointer' }}
      >
        {current?.name ?? allLabel ?? 'Select a project'} ▾
      </button>
      {open && (
        <div
          role="group"
          aria-label="Projects"
          style={{
            position: 'absolute', zIndex: 20, top: '100%', left: 0, marginTop: 4, minWidth: 240, maxWidth: 'min(90vw, 360px)',
            background: 'var(--surface)', color: 'var(--text)', border: '1px solid var(--border)', borderRadius: 6,
            boxShadow: '0 8px 24px rgba(0,0,0,0.35)', display: 'flex', flexDirection: 'column', maxHeight: '60vh',
          }}
        >
          <input
            ref={search}
            type="search"
            aria-label="Search projects"
            placeholder="Search projects"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onSearchKey}
            style={{ margin: 8, padding: '0.375rem 0.5rem', fontSize: '0.8125rem' }}
          />
          <div style={{ overflowY: 'auto', overscrollBehavior: 'contain', paddingBottom: 6 }}>
            {showAll && (
              <div style={row}>
                <button type="button" style={pick} aria-current={value === '0' || value === '' ? 'true' : undefined} onClick={() => choose('0')}>
                  {value === '0' || value === '' ? '✓ ' : ''}{allLabel}
                </button>
              </div>
            )}
            {menu.favourites.length > 0 && <div style={heading}>Favourites</div>}
            {menu.favourites.map(rowFor)}
            {menu.favourites.length > 0 && menu.others.length > 0 && <div style={heading}>All projects</div>}
            {menu.others.map(rowFor)}
            {empty && <div style={{ padding: '0.5rem' }}>No projects match</div>}
          </div>
        </div>
      )}
    </div>
  )
}
