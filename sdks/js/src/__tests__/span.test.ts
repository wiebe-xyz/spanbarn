import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SpanBarn } from '../client.js'
import { Span } from '../span.js'

describe('Span', () => {
  let client: SpanBarn

  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 200 })))
    client = SpanBarn.init({
      endpoint: 'https://test.example.com',
      apiKey: 'test-key',
      service: 'test-service',
      disabled: false,
    })
  })

  afterEach(() => {
    SpanBarn.reset()
    vi.restoreAllMocks()
  })

  it('sets a single attribute', () => {
    const span = client.startSpan('test')
    span.setAttribute('key', 'value')
    span.end()

    expect(client.getQueueLength()).toBe(1)
  })

  it('sets multiple attributes', () => {
    const span = client.startSpan('test')
    span.setAttributes({ a: 1, b: 'two', c: true })
    span.end()

    expect(client.getQueueLength()).toBe(1)
  })

  it('sets ok status', () => {
    const span = client.startSpan('test')
    span.setStatus('ok')
    span.end()

    expect(client.getQueueLength()).toBe(1)
  })

  it('sets error status', () => {
    const span = client.startSpan('test')
    span.setStatus('error')
    span.end()

    expect(client.getQueueLength()).toBe(1)
  })

  it('adds an event with attributes', () => {
    const span = client.startSpan('test')
    span.addEvent('something-happened', { detail: 'info' })
    span.end()

    expect(client.getQueueLength()).toBe(1)
  })

  it('error() sets status and adds error attributes from Error', () => {
    const span = client.startSpan('test')
    const err = new Error('something broke')
    span.error(err)
    span.end()

    expect(client.getQueueLength()).toBe(1)
  })

  it('error() sets status and adds error attributes from string', () => {
    const span = client.startSpan('test')
    span.error('something broke')
    span.end()

    expect(client.getQueueLength()).toBe(1)
  })

  it('end calculates duration and enqueues', () => {
    const span = client.startSpan('test')
    expect(client.getQueueLength()).toBe(0)
    span.end()
    expect(client.getQueueLength()).toBe(1)
  })

  it('second end is a no-op', () => {
    const span = client.startSpan('test')
    span.end()
    span.end()
    expect(client.getQueueLength()).toBe(1)
  })

  it('methods return this for chaining', () => {
    const span = client.startSpan('test')
    const result = span
      .setAttribute('key', 'value')
      .setAttributes({ a: 1 })
      .setStatus('ok')
      .addEvent('event')
      .ok()

    expect(result).toBe(span)
  })

  it('has traceId and spanId', () => {
    const span = client.startSpan('test')
    expect(span.getTraceId()).toMatch(/^[0-9a-f]{32}$/)
    expect(span.getSpanId()).toMatch(/^[0-9a-f]{16}$/)
  })
})
