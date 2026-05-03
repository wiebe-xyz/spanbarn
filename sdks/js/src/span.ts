import type { SpanBarn } from './client.js'
import { generateSpanId } from './ids.js'
import type { SpanAttributes, SpanData, SpanOptions } from './types.js'

function nowMicroseconds(): number {
  return Math.floor(performance.now() * 1000) + Math.floor(Date.now() * 1000)
}

export class Span {
  private data: SpanData
  private ended: boolean = false
  private client: SpanBarn

  constructor(client: SpanBarn, name: string, traceId: string, options?: SpanOptions) {
    this.client = client
    this.data = {
      traceId,
      spanId: generateSpanId(),
      parentSpanId: options?.parentSpanId,
      name,
      service: client.getService(),
      kind: options?.kind ?? 'internal',
      status: 'unset',
      startTime: nowMicroseconds(),
      duration: 0,
      attributes: { ...options?.attributes },
      events: [],
    }
  }

  /** Get the span ID */
  getSpanId(): string {
    return this.data.spanId
  }

  /** Get the trace ID */
  getTraceId(): string {
    return this.data.traceId
  }

  /** Set a single attribute */
  setAttribute(key: string, value: string | number | boolean): this {
    if (!this.ended) {
      this.data.attributes[key] = value
    }
    return this
  }

  /** Set multiple attributes */
  setAttributes(attrs: SpanAttributes): this {
    if (!this.ended) {
      Object.assign(this.data.attributes, attrs)
    }
    return this
  }

  /** Set span status */
  setStatus(status: 'ok' | 'error'): this {
    if (!this.ended) {
      this.data.status = status
    }
    return this
  }

  /** Add an event to the span */
  addEvent(name: string, attributes?: SpanAttributes): this {
    if (!this.ended) {
      this.data.events.push({
        name,
        time: nowMicroseconds(),
        attributes,
      })
    }
    return this
  }

  /** Shorthand for setStatus('ok') */
  ok(): this {
    return this.setStatus('ok')
  }

  /** Set error status and add error attributes */
  error(err?: Error | string): this {
    this.setStatus('error')
    if (!this.ended) {
      if (err instanceof Error) {
        this.data.attributes['error.type'] = err.name
        this.data.attributes['error.message'] = err.message
        if (err.stack) {
          this.data.attributes['error.stack'] = err.stack
        }
      } else if (typeof err === 'string') {
        this.data.attributes['error.message'] = err
      }
    }
    return this
  }

  /** End the span, calculating duration and enqueueing */
  end(): void {
    if (this.ended) return
    this.ended = true
    this.data.duration = nowMicroseconds() - this.data.startTime
    this.client.enqueue(this.data)
  }

  /** Check if the span has ended (for testing) */
  isEnded(): boolean {
    return this.ended
  }
}
