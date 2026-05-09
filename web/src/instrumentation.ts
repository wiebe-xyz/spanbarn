const TELEMETRY_ENDPOINT = '/api/v1/telemetry'
const CLIENT_ERRORS_ENDPOINT = '/api/v1/client-errors'
const SERVICE_NAME = 'spanbarn-web'

const spanQueue: SpanPayload[] = []
let flushTimer: ReturnType<typeof setInterval> | null = null

let pageTraceId = ''
let pageSpanId = ''

type SpanPayload = {
  traceId: string
  spanId: string
  parentSpanId?: string
  name: string
  service: string
  kind: string
  status: string
  startTime: number
  duration: number
  attributes: Record<string, string | number | boolean>
}

function hex(bytes: number): string {
  const arr = crypto.getRandomValues(new Uint8Array(bytes))
  return Array.from(arr, b => b.toString(16).padStart(2, '0')).join('')
}

function nowUs(): number {
  return Math.round(performance.timeOrigin * 1000 + performance.now() * 1000)
}

function traceparent(traceId: string, spanId: string): string {
  return `00-${traceId}-${spanId}-01`
}

function startPageTrace(path: string, fromPath?: string) {
  flushSpans()

  pageTraceId = hex(16)
  pageSpanId = hex(8)

  const attrs: Record<string, string | number | boolean> = {
    'navigation.to': path,
  }
  if (fromPath) attrs['navigation.from'] = fromPath

  enqueueSpan({
    traceId: pageTraceId,
    spanId: pageSpanId,
    name: `page ${path}`,
    service: SERVICE_NAME,
    kind: 'INTERNAL',
    status: 'OK',
    startTime: nowUs(),
    duration: 1,
    attributes: attrs,
  })
}

function reportError(error: Error, attrs?: Record<string, string>) {
  fetch(CLIENT_ERRORS_ENDPOINT, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify({
      message: error.message,
      type: error.name || 'Error',
      stack: error.stack ?? '',
      url: location.href,
      attributes: attrs,
    }),
    keepalive: true,
  }).catch(() => {})
}

function enqueueSpan(span: SpanPayload) {
  spanQueue.push(span)
  if (spanQueue.length >= 25) flushSpans()
}

function flushSpans() {
  if (spanQueue.length === 0) return
  const batch = spanQueue.splice(0, 50)
  fetch(TELEMETRY_ENDPOINT, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ spans: batch }),
    credentials: 'same-origin',
    keepalive: true,
  }).catch(() => {})
}

function isInstrumentationUrl(url: string): boolean {
  return url.includes(TELEMETRY_ENDPOINT) || url.includes(CLIENT_ERRORS_ENDPOINT)
}

function installErrorHandlers() {
  window.addEventListener('error', (event) => {
    if (event.error instanceof Error) {
      reportError(event.error, { source: event.filename ?? '' })
    }
  })

  window.addEventListener('unhandledrejection', (event) => {
    const error = event.reason instanceof Error
      ? event.reason
      : new Error(String(event.reason))
    reportError(error, { type: 'unhandledrejection' })
  })
}

function instrumentFetch() {
  const originalFetch = window.fetch
  window.fetch = function (input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    const method = init?.method ?? 'GET'

    if (isInstrumentationUrl(url)) {
      return originalFetch.call(this, input, init)
    }

    const spanId = hex(8)
    const startUs = nowUs()

    const headers = new Headers(init?.headers)
    if (pageTraceId) {
      headers.set('traceparent', traceparent(pageTraceId, spanId))
    }
    const patchedInit = { ...init, headers }

    return originalFetch.call(this, input, patchedInit).then(
      (response) => {
        enqueueSpan({
          traceId: pageTraceId,
          spanId,
          parentSpanId: pageSpanId,
          name: `${method} ${new URL(url, location.origin).pathname}`,
          service: SERVICE_NAME,
          kind: 'CLIENT',
          status: response.ok ? 'OK' : 'ERROR',
          startTime: startUs,
          duration: nowUs() - startUs,
          attributes: {
            'http.method': method,
            'http.url': url,
            'http.status_code': response.status,
          },
        })

        if (!response.ok && response.status >= 500) {
          reportError(
            new Error(`HTTP ${response.status} ${method} ${url}`),
            { type: 'http_error', status: String(response.status) },
          )
        }

        return response
      },
      (error) => {
        enqueueSpan({
          traceId: pageTraceId,
          spanId,
          parentSpanId: pageSpanId,
          name: `${method} ${new URL(url, location.origin).pathname}`,
          service: SERVICE_NAME,
          kind: 'CLIENT',
          status: 'ERROR',
          startTime: startUs,
          duration: nowUs() - startUs,
          attributes: {
            'http.method': method,
            'http.url': url,
            'error.message': error instanceof Error ? error.message : String(error),
          },
        })
        throw error
      },
    )
  }
}

function instrumentNavigation() {
  let lastPath = location.pathname
  const originalPushState = history.pushState.bind(history)
  const originalReplaceState = history.replaceState.bind(history)

  startPageTrace(lastPath)

  function onNav() {
    const newPath = location.pathname
    if (newPath === lastPath) return
    const fromPath = lastPath
    lastPath = newPath
    startPageTrace(newPath, fromPath)
  }

  history.pushState = function (...args: Parameters<typeof history.pushState>) {
    originalPushState(...args)
    onNav()
  }
  history.replaceState = function (...args: Parameters<typeof history.replaceState>) {
    originalReplaceState(...args)
    onNav()
  }
  window.addEventListener('popstate', onNav)
}

export function initInstrumentation() {
  installErrorHandlers()
  instrumentFetch()
  instrumentNavigation()
  flushTimer = setInterval(flushSpans, 5000)

  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') flushSpans()
  })
}

export function shutdownInstrumentation() {
  if (flushTimer) {
    clearInterval(flushTimer)
    flushTimer = null
  }
  flushSpans()
}
