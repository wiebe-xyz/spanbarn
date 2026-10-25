// A tab that stays open across a deploy still points at hashed chunks the new
// build removed. Vite fires `vite:preloadError` when such a lazy import fails.
// One reload fetches the new index and fixes it, so that is the response. The
// reload is guarded in sessionStorage so a chunk that stays missing cannot loop.
// Only a failure that survives a reload is reported, under one fixed type and
// message so every chunk lands in one issue; the chunk URL is an attribute.

export const STALE_CHUNK_KEY = 'spanbarn:stale-chunk-reload'
export const STALE_CHUNK_WINDOW_MS = 30_000
export const STALE_CHUNK_TYPE = 'StaleChunk'
export const STALE_CHUNK_MESSAGE = 'Lazy chunk still missing after a reload'

export type StaleChunkDeps = {
  storage: Pick<Storage, 'getItem' | 'setItem'> | null
  now: () => number
  reload: () => void
  report: (error: Error, attrs: Record<string, string>) => void
}

/** Vite's message is "Unable to preload CSS for <url>" (or JS). */
export function chunkUrlFrom(payload: unknown): string {
  const message = payload instanceof Error ? payload.message : String(payload ?? '')
  const match = / for (\S+)/.exec(message)
  return match?.[1] ?? ''
}

function reloadedRecently(deps: StaleChunkDeps): boolean {
  try {
    const at = Number(deps.storage?.getItem(STALE_CHUNK_KEY) ?? 0)
    return Number.isFinite(at) && at > 0 && deps.now() - at < STALE_CHUNK_WINDOW_MS
  } catch {
    return true // storage unreadable: do not risk a reload loop
  }
}

export function handleStaleChunk(event: Event & { payload?: unknown }, deps: StaleChunkDeps): void {
  // Without this Vite rethrows the error, which is what filed SPA-75.
  event.preventDefault()
  if (!deps.storage || reloadedRecently(deps)) {
    const error = new Error(STALE_CHUNK_MESSAGE)
    error.name = STALE_CHUNK_TYPE
    error.stack = ''
    deps.report(error, { type: STALE_CHUNK_TYPE, chunk: chunkUrlFrom(event.payload) })
    return
  }
  try {
    deps.storage.setItem(STALE_CHUNK_KEY, String(deps.now()))
  } catch {
    return // cannot record the guard, so do not reload
  }
  deps.reload()
}

export function installStaleChunkHandler(report: StaleChunkDeps['report']): void {
  window.addEventListener('vite:preloadError', (event) => {
    let storage: StaleChunkDeps['storage'] = null
    try {
      storage = window.sessionStorage
    } catch {
      /* blocked: handleStaleChunk then never reloads */
    }
    handleStaleChunk(event as Event & { payload?: unknown }, {
      storage,
      now: () => Date.now(),
      reload: () => window.location.reload(),
      report,
    })
  })
}
