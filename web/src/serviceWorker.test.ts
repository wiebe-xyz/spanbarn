import { describe, expect, it, vi } from 'vitest'
import source from '../public/sw.js?raw'

type FetchListener = (event: { request: { url: string; method: string; mode: string }; respondWith: (p: unknown) => void }) => void

function loadFetchListener(): FetchListener {
  const listeners: Record<string, FetchListener> = {}
  const fakeSelf = {
    addEventListener: (type: string, fn: FetchListener) => { listeners[type] = fn },
    skipWaiting: () => {},
    clients: { claim: () => {} },
  }
  new Function('self', 'caches', 'fetch', source)(fakeSelf, { open: () => new Promise(() => {}) }, vi.fn())
  return listeners.fetch
}

function respondsTo(path: string, method = 'GET'): boolean {
  const respondWith = vi.fn()
  loadFetchListener()({
    request: { url: `https://spanbarn.test${path}`, method, mode: 'cors' },
    respondWith,
  })
  return respondWith.mock.calls.length > 0
}

describe('service worker API caching', () => {
  it('serves the aggregate read endpoints stale-while-revalidate', () => {
    expect(respondsTo('/api/v1/services?from=a&to=b')).toBe(true)
    expect(respondsTo('/api/v1/services/checkout/operations?from=a')).toBe(true)
    expect(respondsTo('/api/v1/dependencies')).toBe(true)
  })

  it('leaves user-edited resources to the network so a save is visible on reload', () => {
    expect(respondsTo('/api/v1/boards?project_id=1')).toBe(false)
    expect(respondsTo('/api/v1/boards/4')).toBe(false)
    expect(respondsTo('/api/v1/alerts')).toBe(false)
    expect(respondsTo('/api/v1/projects')).toBe(false)
    expect(respondsTo('/api/v1/health')).toBe(false)
  })

  it('never intercepts writes', () => {
    expect(respondsTo('/api/v1/services', 'POST')).toBe(false)
  })
})
