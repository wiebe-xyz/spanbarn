import { generateTraceId } from './ids.js'
import { Span } from './span.js'
import { Transport } from './transport.js'
import type { SpanBarnConfig, SpanData, SpanOptions } from './types.js'

type ResolvedConfig = {
  endpoint: string
  apiKey: string
  service: string
  environment: string
  flushInterval: number
  maxBatchSize: number
  maxQueueSize: number
  debug: boolean
  disabled: boolean
  beforeSend?: (span: SpanData) => SpanData | null
}

export class SpanBarn {
  private static instance: SpanBarn | null = null

  private config: ResolvedConfig
  private queue: SpanData[] = []
  private flushTimer: ReturnType<typeof setInterval> | null = null
  private transport: Transport
  private traceId: string

  private constructor(config: SpanBarnConfig) {
    this.config = {
      endpoint: config.endpoint,
      apiKey: config.apiKey,
      service: config.service,
      environment: config.environment ?? 'production',
      flushInterval: config.flushInterval ?? 5000,
      maxBatchSize: config.maxBatchSize ?? 100,
      maxQueueSize: config.maxQueueSize ?? 1000,
      debug: config.debug ?? false,
      disabled: config.disabled ?? false,
      beforeSend: config.beforeSend,
    }
    this.transport = new Transport(this.config.endpoint, this.config.apiKey)
    this.traceId = generateTraceId()

    if (!this.config.disabled) {
      this.flushTimer = setInterval(() => {
        void this.flush()
      }, this.config.flushInterval)
      // Unref if available (Node.js) so timer doesn't keep process alive
      if (this.flushTimer && typeof this.flushTimer === 'object' && 'unref' in this.flushTimer) {
        ;(this.flushTimer as { unref: () => void }).unref()
      }
    }
  }

  /** Initialize the SpanBarn client (singleton) */
  static init(config: SpanBarnConfig): SpanBarn {
    SpanBarn.instance = new SpanBarn(config)
    return SpanBarn.instance
  }

  /** Get the current singleton instance */
  static getInstance(): SpanBarn {
    if (!SpanBarn.instance) {
      throw new Error('SpanBarn not initialized. Call SpanBarn.init() first.')
    }
    return SpanBarn.instance
  }

  /** Reset the singleton (for testing) */
  static reset(): void {
    if (SpanBarn.instance) {
      void SpanBarn.instance.shutdown()
    }
    SpanBarn.instance = null
  }

  /** Get the service name */
  getService(): string {
    return this.config.service
  }

  /** Get the current trace ID */
  getTraceId(): string {
    return this.traceId
  }

  /** Get current queue length (for testing) */
  getQueueLength(): number {
    return this.queue.length
  }

  /** Get the resolved config (for testing) */
  getConfig(): ResolvedConfig {
    return this.config
  }

  /** Start a new span */
  startSpan(name: string, options?: SpanOptions): Span {
    return new Span(this, name, this.traceId, options)
  }

  /** Add a completed span to the queue */
  enqueue(span: SpanData): void {
    if (this.config.disabled) return

    let finalSpan: SpanData | null = span

    if (this.config.beforeSend) {
      finalSpan = this.config.beforeSend(span)
      if (!finalSpan) {
        if (this.config.debug) {
          console.debug('[SpanBarn] Span dropped by beforeSend filter:', span.name)
        }
        return
      }
    }

    // Enforce max queue size — drop oldest
    if (this.queue.length >= this.config.maxQueueSize) {
      this.queue.shift()
      if (this.config.debug) {
        console.debug('[SpanBarn] Queue full, dropping oldest span')
      }
    }

    this.queue.push(finalSpan)

    if (this.config.debug) {
      console.debug('[SpanBarn] Enqueued span:', finalSpan.name, `(queue: ${this.queue.length})`)
    }

    // Flush immediately if we've hit the batch size
    if (this.queue.length >= this.config.maxBatchSize) {
      void this.flush()
    }
  }

  /** Send queued spans to the server */
  async flush(): Promise<void> {
    if (this.queue.length === 0) return

    const batch = this.queue.splice(0, this.config.maxBatchSize)

    if (this.config.debug) {
      console.debug(`[SpanBarn] Flushing ${batch.length} spans`)
    }

    const success = await this.transport.send(batch)

    if (!success) {
      if (this.config.debug) {
        console.debug('[SpanBarn] Flush failed, re-queuing spans')
      }
      // Re-queue failed spans at the front (respecting max queue size)
      const available = this.config.maxQueueSize - this.queue.length
      if (available > 0) {
        this.queue.unshift(...batch.slice(0, available))
      }
    }
  }

  /** Shutdown the client: flush remaining spans and clear timer */
  async shutdown(): Promise<void> {
    if (this.flushTimer) {
      clearInterval(this.flushTimer)
      this.flushTimer = null
    }
    await this.flush()
  }
}
