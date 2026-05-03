export type SpanBarnConfig = {
  endpoint: string
  apiKey: string
  service: string
  environment?: string
  flushInterval?: number // ms, default 5000
  maxBatchSize?: number // default 100
  maxQueueSize?: number // default 1000
  debug?: boolean
  disabled?: boolean
  beforeSend?: (span: SpanData) => SpanData | null // filter/modify spans
}

export type SpanOptions = {
  kind?: 'server' | 'client' | 'internal' | 'producer' | 'consumer'
  attributes?: SpanAttributes
  parentSpanId?: string
}

export type SpanAttributes = Record<string, string | number | boolean>

export type SpanData = {
  traceId: string
  spanId: string
  parentSpanId?: string
  name: string
  service: string
  resource?: string
  kind: string
  status: 'ok' | 'error' | 'unset'
  startTime: number // unix microseconds
  duration: number // microseconds
  attributes: SpanAttributes
  events: SpanEvent[]
}

export type SpanEvent = {
  name: string
  time: number // unix microseconds
  attributes?: SpanAttributes
}
