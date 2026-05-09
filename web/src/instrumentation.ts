const TELEMETRY_ENDPOINT = '/api/v1/telemetry'
const CLIENT_ERRORS_ENDPOINT = '/api/v1/client-errors'
const SERVICE_NAME = 'spanbarn-web'

const spanQueue: SpanPayload[] = []
let flushTimer: ReturnType<typeof setInterval> | null = null

let pageTraceId = ''
let pageSpanId = ''
let pageStartUs = 0
let pendingPageSpan: SpanPayload | null = null

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

function finalizePageSpan() {
  if (pendingPageSpan) {
    pendingPageSpan.duration = nowUs() - pendingPageSpan.startTime
    spanQueue.push(pendingPageSpan)
    pendingPageSpan = null
  }
}

function startPageTrace(path: string, fromPath?: string) {
  finalizePageSpan()
  flushSpans()

  pageTraceId = hex(16)
  pageSpanId = hex(8)
  pageStartUs = nowUs()

  const attrs: Record<string, string | number | boolean> = {
    'navigation.to': path,
  }
  if (fromPath) attrs['navigation.from'] = fromPath

  pendingPageSpan = {
    traceId: pageTraceId,
    spanId: pageSpanId,
    name: `page ${path}`,
    service: SERVICE_NAME,
    kind: 'INTERNAL',
    status: 'OK',
    startTime: pageStartUs,
    duration: 0,
    attributes: attrs,
  }
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
  finalizePageSpan()
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

function observeWebVitals() {
  function reportVital(name: string, valueMs: number, rating: string) {
    const spanId = hex(8)
    enqueueSpan({
      traceId: pageTraceId || hex(16),
      spanId,
      parentSpanId: pageSpanId || undefined,
      name: `webvital.${name}`,
      service: SERVICE_NAME,
      kind: 'INTERNAL',
      status: 'OK',
      startTime: nowUs(),
      duration: Math.round(valueMs * 1000),
      attributes: {
        'webvital.name': name,
        'webvital.value_ms': Math.round(valueMs * 100) / 100,
        'webvital.rating': rating,
        'webvital.page': location.pathname,
      },
    })
  }

  function rate(name: string, value: number): string {
    const thresholds: Record<string, [number, number]> = {
      LCP: [2500, 4000],
      FCP: [1800, 3000],
      TTFB: [800, 1800],
      CLS: [0.1, 0.25],
      INP: [200, 500],
    }
    const [good, poor] = thresholds[name] ?? [1000, 3000]
    if (value <= good) return 'good'
    if (value <= poor) return 'needs-improvement'
    return 'poor'
  }

  try {
    const po = new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        if (entry.entryType === 'largest-contentful-paint') {
          reportVital('LCP', entry.startTime, rate('LCP', entry.startTime))
        }
      }
    })
    po.observe({ type: 'largest-contentful-paint', buffered: true })
  } catch { /* not supported */ }

  try {
    const po = new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        if (entry.entryType === 'paint' && entry.name === 'first-contentful-paint') {
          reportVital('FCP', entry.startTime, rate('FCP', entry.startTime))
        }
      }
    })
    po.observe({ type: 'paint', buffered: true })
  } catch { /* not supported */ }

  try {
    const po = new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        if (entry.entryType === 'navigation') {
          const nav = entry as PerformanceNavigationTiming
          const ttfb = nav.responseStart - nav.requestStart
          if (ttfb > 0) reportVital('TTFB', ttfb, rate('TTFB', ttfb))
        }
      }
    })
    po.observe({ type: 'navigation', buffered: true })
  } catch { /* not supported */ }

  try {
    const po = new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        if (entry.entryType === 'layout-shift' && !(entry as LayoutShift).hadRecentInput) {
          const value = (entry as LayoutShift).value
          reportVital('CLS', value * 1000, rate('CLS', value))
        }
      }
    })
    po.observe({ type: 'layout-shift', buffered: true })
  } catch { /* not supported */ }
}

interface LayoutShift extends PerformanceEntry {
  hadRecentInput: boolean
  value: number
}

export function initInstrumentation() {
  installErrorHandlers()
  instrumentFetch()
  instrumentNavigation()
  observeWebVitals()
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
