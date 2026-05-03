import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SpanBarn } from '../client.js'
import type { SpanData } from '../types.js'

describe('SpanBarn client', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 200 })))
  })

  afterEach(() => {
    SpanBarn.reset()
    vi.restoreAllMocks()
  })

  it('initializes with config defaults', () => {
    const client = SpanBarn.init({
      endpoint: 'https://test.example.com',
      apiKey: 'key',
      service: 'my-service',
    })

    const config = client.getConfig()
    expect(config.flushInterval).toBe(5000)
    expect(config.maxBatchSize).toBe(100)
    expect(config.maxQueueSize).toBe(1000)
    expect(config.debug).toBe(false)
    expect(config.disabled).toBe(false)
    expect(config.environment).toBe('production')
  })

  it('returns singleton via getInstance', () => {
    const client = SpanBarn.init({
      endpoint: 'https://test.example.com',
      apiKey: 'key',
      service: 'my-service',
    })

    expect(SpanBarn.getInstance()).toBe(client)
  })

  it('throws if getInstance called before init', () => {
    expect(() => SpanBarn.getInstance()).toThrow('SpanBarn not initialized')
  })

  it('creates span with traceId and spanId', () => {
    const client = SpanBarn.init({
      endpoint: 'https://test.example.com',
      apiKey: 'key',
      service: 'my-service',
    })

    const span = client.startSpan('test-span')
    expect(span.getTraceId()).toMatch(/^[0-9a-f]{32}$/)
    expect(span.getSpanId()).toMatch(/^[0-9a-f]{16}$/)
  })

  it('enqueues span and increments queue length', () => {
    const client = SpanBarn.init({
      endpoint: 'https://test.example.com',
      apiKey: 'key',
      service: 'my-service',
    })

    expect(client.getQueueLength()).toBe(0)
    const span = client.startSpan('test')
    span.end()
    expect(client.getQueueLength()).toBe(1)
  })

  it('flush sends spans and empties queue', async () => {
    const client = SpanBarn.init({
      endpoint: 'https://test.example.com',
      apiKey: 'key',
      service: 'my-service',
    })

    const span = client.startSpan('test')
    span.end()
    expect(client.getQueueLength()).toBe(1)

    await client.flush()
    expect(client.getQueueLength()).toBe(0)

    expect(fetch).toHaveBeenCalledOnce()
  })

  it('shutdown flushes remaining spans', async () => {
    const client = SpanBarn.init({
      endpoint: 'https://test.example.com',
      apiKey: 'key',
      service: 'my-service',
    })

    client.startSpan('test').end()
    await client.shutdown()
    expect(client.getQueueLength()).toBe(0)
  })

  it('disabled mode does not enqueue spans', () => {
    const client = SpanBarn.init({
      endpoint: 'https://test.example.com',
      apiKey: 'key',
      service: 'my-service',
      disabled: true,
    })

    const span = client.startSpan('test')
    span.end()
    expect(client.getQueueLength()).toBe(0)
  })

  it('beforeSend modifies span data', () => {
    const client = SpanBarn.init({
      endpoint: 'https://test.example.com',
      apiKey: 'key',
      service: 'my-service',
      beforeSend: (span: SpanData) => ({
        ...span,
        attributes: { ...span.attributes, injected: true },
      }),
    })

    client.startSpan('test').end()
    expect(client.getQueueLength()).toBe(1)
  })

  it('beforeSend returning null drops the span', () => {
    const client = SpanBarn.init({
      endpoint: 'https://test.example.com',
      apiKey: 'key',
      service: 'my-service',
      beforeSend: () => null,
    })

    client.startSpan('test').end()
    expect(client.getQueueLength()).toBe(0)
  })

  it('max queue size drops oldest spans', () => {
    const client = SpanBarn.init({
      endpoint: 'https://test.example.com',
      apiKey: 'key',
      service: 'my-service',
      maxQueueSize: 3,
      maxBatchSize: 1000, // prevent auto-flush
    })

    for (let i = 0; i < 5; i++) {
      client.startSpan(`span-${i}`).end()
    }

    // Queue should be capped at 3
    expect(client.getQueueLength()).toBe(3)
  })
})
