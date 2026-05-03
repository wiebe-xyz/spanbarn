import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Transport } from '../transport.js'
import type { SpanData } from '../types.js'

const mockSpan: SpanData = {
  traceId: 'a'.repeat(32),
  spanId: 'b'.repeat(16),
  name: 'test-span',
  service: 'test-service',
  kind: 'internal',
  status: 'ok',
  startTime: 1000000,
  duration: 500,
  attributes: {},
  events: [],
}

describe('Transport', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn())
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('sends spans with correct request format', async () => {
    const mockFetch = vi.mocked(fetch)
    mockFetch.mockResolvedValue(new Response(null, { status: 200 }))

    const transport = new Transport('https://spanbarn.example.com', 'test-key')
    const result = await transport.send([mockSpan])

    expect(result).toBe(true)
    expect(mockFetch).toHaveBeenCalledWith(
      'https://spanbarn.example.com/api/v1/spans',
      {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-SpanBarn-Api-Key': 'test-key',
        },
        body: JSON.stringify({ spans: [mockSpan] }),
      }
    )
  })

  it('strips trailing slash from endpoint', async () => {
    const mockFetch = vi.mocked(fetch)
    mockFetch.mockResolvedValue(new Response(null, { status: 200 }))

    const transport = new Transport('https://spanbarn.example.com/', 'key')
    await transport.send([mockSpan])

    expect(mockFetch).toHaveBeenCalledWith(
      'https://spanbarn.example.com/api/v1/spans',
      expect.any(Object)
    )
  })

  it('returns false on fetch failure without throwing', async () => {
    const mockFetch = vi.mocked(fetch)
    mockFetch.mockRejectedValue(new Error('Network error'))

    const transport = new Transport('https://spanbarn.example.com', 'key')
    const result = await transport.send([mockSpan])

    expect(result).toBe(false)
  })

  it('returns false on non-ok response', async () => {
    const mockFetch = vi.mocked(fetch)
    mockFetch.mockResolvedValue(new Response(null, { status: 500 }))

    const transport = new Transport('https://spanbarn.example.com', 'key')
    const result = await transport.send([mockSpan])

    expect(result).toBe(false)
  })
})
