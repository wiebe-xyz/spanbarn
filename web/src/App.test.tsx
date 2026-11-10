import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import App from './App'

const originalLocation = window.location
let urls: string[]

function stubFetch(meStatus: number) {
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      urls.push(url)
      if (url === '/api/v1/me') {
        return Promise.resolve(
          meStatus === 200
            ? new Response(JSON.stringify({ display_name: 'wiebe' }))
            : new Response(JSON.stringify({ error: 'unauthorized' }), { status: meStatus }),
        )
      }
      // A signed-out server answers every API read with 401, like the real one.
      if (meStatus !== 200 && url.startsWith('/api/v1/') && url !== '/api/v1/client-config') {
        return Promise.resolve(new Response(JSON.stringify({ error: 'unauthorized' }), { status: 401 }))
      }
      if (url.startsWith('/api/v1/dashboard/heatmap')) {
        return Promise.resolve(new Response(JSON.stringify({ cells: [], timeBuckets: [], durationBuckets: [] })))
      }
      return Promise.resolve(new Response('[]'))
    }),
  )
}

beforeEach(() => {
  urls = []
  window.history.pushState({}, '', '/')
  // fetchJSON sends a 401 to /login by assigning location.href.
  Object.defineProperty(window, 'location', {
    value: { ...originalLocation, href: '/', pathname: '/', search: '', assign: vi.fn() },
    writable: true,
    configurable: true,
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  Object.defineProperty(window, 'location', { value: originalLocation, writable: true, configurable: true })
})

const dashboardData = (u: string) => u.startsWith('/api/v1/dashboard') || u.startsWith('/api/v1/projects')

describe('App signed out', () => {
  it('goes to sign-in before any dashboard or projects request', async () => {
    stubFetch(401)
    render(<App />)
    await waitFor(() => expect(window.location.href).toBe('/login'))
    await new Promise((r) => setTimeout(r, 50))
    expect(urls.filter(dashboardData)).toEqual([])
  })
})

describe('App signed in', () => {
  it('renders the dashboard and loads its data', async () => {
    stubFetch(200)
    render(<App />)
    await waitFor(() => expect(screen.getAllByRole('navigation').length).toBeGreaterThan(0))
    await waitFor(() => expect(urls.some(dashboardData)).toBe(true))
  })
})
