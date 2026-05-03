import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { fetchJSON, ApiError } from './client'

// Mock fetch globally
const mockFetch = vi.fn()
vi.stubGlobal('fetch', mockFetch)

describe('fetchJSON', () => {
  const originalLocation = window.location

  beforeEach(() => {
    mockFetch.mockReset()
    // Mock window.location
    Object.defineProperty(window, 'location', {
      value: { ...originalLocation, href: '', pathname: '/dashboard' },
      writable: true,
      configurable: true,
    })
  })

  afterEach(() => {
    Object.defineProperty(window, 'location', {
      value: originalLocation,
      writable: true,
      configurable: true,
    })
  })

  it('returns parsed JSON on success', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      text: () => Promise.resolve('{"foo":"bar"}'),
    })

    const result = await fetchJSON<{ foo: string }>('/api/test')
    expect(result).toEqual({ foo: 'bar' })
    expect(mockFetch).toHaveBeenCalledWith('/api/test', expect.objectContaining({
      credentials: 'same-origin',
    }))
  })

  it('redirects to /login on 401', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: false,
      status: 401,
      json: () => Promise.resolve({ error: 'unauthorized' }),
    })

    await expect(fetchJSON('/api/test')).rejects.toThrow(ApiError)
    expect(window.location.href).toBe('/login')
  })

  it('does not redirect if already on /login', async () => {
    Object.defineProperty(window, 'location', {
      value: { ...originalLocation, href: '/login', pathname: '/login' },
      writable: true,
      configurable: true,
    })

    mockFetch.mockResolvedValueOnce({
      ok: false,
      status: 401,
      json: () => Promise.resolve({ error: 'unauthorized' }),
    })

    await expect(fetchJSON('/api/test')).rejects.toThrow(ApiError)
    // href should remain /login — no redirect
    expect(window.location.href).toBe('/login')
  })

  it('throws ApiError with message from response body', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: false,
      status: 400,
      json: () => Promise.resolve({ error: 'bad request' }),
    })

    try {
      await fetchJSON('/api/test')
      expect.fail('should have thrown')
    } catch (err) {
      expect(err).toBeInstanceOf(ApiError)
      expect((err as ApiError).status).toBe(400)
      expect((err as ApiError).message).toBe('bad request')
    }
  })

  it('returns undefined for empty response body', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 204,
      text: () => Promise.resolve(''),
    })

    const result = await fetchJSON('/api/test')
    expect(result).toBeUndefined()
  })
})

describe('api methods construct correct URLs', () => {
  beforeEach(() => {
    mockFetch.mockReset()
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      text: () => Promise.resolve('[]'),
    })
  })

  it('getServices includes time range params', async () => {
    const { api } = await import('./client')
    await api.getServices('2024-01-01T00:00:00Z', '2024-01-02T00:00:00Z')
    expect(mockFetch).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/services?'),
      expect.anything(),
    )
    const url = mockFetch.mock.calls[0][0] as string
    expect(url).toContain('from=')
    expect(url).toContain('to=')
  })

  it('getOperations includes service in path', async () => {
    const { api } = await import('./client')
    await api.getOperations('my-service', '2024-01-01T00:00:00Z', '2024-01-02T00:00:00Z')
    const url = mockFetch.mock.calls[0][0] as string
    expect(url).toContain('/api/v1/services/my-service/operations')
  })

  it('getTimeseries includes service and operation in path', async () => {
    const { api } = await import('./client')
    await api.getTimeseries('svc', 'op', '2024-01-01T00:00:00Z', '2024-01-02T00:00:00Z')
    const url = mockFetch.mock.calls[0][0] as string
    expect(url).toContain('/api/v1/services/svc/operations/op/timeseries')
  })

  it('searchTraces constructs query params', async () => {
    const { api } = await import('./client')
    await api.searchTraces({
      service: 'svc',
      operation: 'op',
      from: '2024-01-01T00:00:00Z',
      to: '2024-01-02T00:00:00Z',
      limit: 10,
    })
    const url = mockFetch.mock.calls[0][0] as string
    expect(url).toContain('/api/v1/traces?')
    expect(url).toContain('service=svc')
    expect(url).toContain('limit=10')
  })

  it('getTrace includes traceId in path', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      text: () => Promise.resolve('{"traceId":"abc123","spans":[]}'),
    })
    const { api } = await import('./client')
    await api.getTrace('abc123')
    const url = mockFetch.mock.calls[0][0] as string
    expect(url).toBe('/api/v1/traces/abc123')
  })
})
