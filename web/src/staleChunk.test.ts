import { describe, expect, it, vi } from 'vitest'
import {
  STALE_CHUNK_KEY,
  STALE_CHUNK_MESSAGE,
  STALE_CHUNK_TYPE,
  STALE_CHUNK_WINDOW_MS,
  chunkUrlFrom,
  handleStaleChunk,
  type StaleChunkDeps,
} from './staleChunk'

function setup(stored?: string, now = 1_000_000) {
  const data = new Map<string, string>(stored === undefined ? [] : [[STALE_CHUNK_KEY, stored]])
  const deps = {
    storage: {
      getItem: (k: string) => data.get(k) ?? null,
      setItem: (k: string, v: string) => void data.set(k, v),
    },
    now: () => now,
    reload: vi.fn(),
    report: vi.fn(),
  } satisfies StaleChunkDeps
  const event = new Event('vite:preloadError', { cancelable: true }) as Event & { payload?: unknown }
  event.payload = new Error('Unable to preload CSS for https://spanbarn.test/assets/TraceDetailPage-abc123.css')
  return { data, deps, event }
}

describe('stale chunk handling', () => {
  it('reloads once and does not report on the first failure', () => {
    const { data, deps, event } = setup()
    handleStaleChunk(event, deps)
    expect(deps.reload).toHaveBeenCalledTimes(1)
    expect(deps.report).not.toHaveBeenCalled()
    expect(data.get(STALE_CHUNK_KEY)).toBe('1000000')
    expect(event.defaultPrevented).toBe(true)
  })

  it('does not reload again inside the window and reports under one fixed type and message', () => {
    const { deps, event } = setup(String(1_000_000 - 1000))
    handleStaleChunk(event, deps)
    expect(deps.reload).not.toHaveBeenCalled()
    expect(deps.report).toHaveBeenCalledTimes(1)
    const [error, attrs] = deps.report.mock.calls[0]
    expect(error.name).toBe(STALE_CHUNK_TYPE)
    expect(error.message).toBe(STALE_CHUNK_MESSAGE)
    expect(error.stack).toBe('')
    expect(attrs.chunk).toBe('https://spanbarn.test/assets/TraceDetailPage-abc123.css')
  })

  it('reports the same message for different chunks', () => {
    const a = setup(String(1_000_000 - 1000))
    const b = setup(String(1_000_000 - 1000))
    b.event.payload = new Error('Unable to preload CSS for https://spanbarn.test/assets/Other-def456.css')
    handleStaleChunk(a.event, a.deps)
    handleStaleChunk(b.event, b.deps)
    expect(a.deps.report.mock.calls[0][0].message).toBe(b.deps.report.mock.calls[0][0].message)
  })

  it('reloads again once the window has passed (a later deploy)', () => {
    const { deps, event } = setup(String(1_000_000 - STALE_CHUNK_WINDOW_MS - 1))
    handleStaleChunk(event, deps)
    expect(deps.reload).toHaveBeenCalledTimes(1)
  })

  it('never reloads when sessionStorage is unavailable', () => {
    const { deps, event } = setup()
    handleStaleChunk(event, { ...deps, storage: null })
    expect(deps.reload).not.toHaveBeenCalled()
    expect(deps.report).toHaveBeenCalledTimes(1)
  })

  it('does not reload when the guard cannot be written', () => {
    const { deps, event } = setup()
    const storage = { getItem: () => null, setItem: () => { throw new Error('quota') } }
    handleStaleChunk(event, { ...deps, storage })
    expect(deps.reload).not.toHaveBeenCalled()
  })

  it('reads the chunk URL from the preload message', () => {
    expect(chunkUrlFrom(new Error('Unable to preload CSS for /assets/a-1.css'))).toBe('/assets/a-1.css')
    expect(chunkUrlFrom('nothing here')).toBe('')
    expect(chunkUrlFrom(undefined)).toBe('')
  })
})
