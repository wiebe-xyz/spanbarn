import { SpanBarn } from './client.js'
import { generateTraceId, generateSpanId } from './ids.js'
import { makeTraceparent } from './context.js'
import type { SpanData } from './types.js'

export type BrowserInstrumentationConfig = {
  webVitals?: boolean
  fetch?: boolean
  navigation?: boolean
  errors?: boolean
  errorEndpoint?: string
}

let pageTraceId = ''
let pageSpanId = ''
let pendingPageSpan: SpanData | null = null
let currentPath = ''

function nowUs(): number {
  return Math.round(performance.timeOrigin * 1000 + performance.now() * 1000)
}

function getClient(): SpanBarn {
  return SpanBarn.getInstance()
}

function finalizePageSpan() {
  if (pendingPageSpan) {
    pendingPageSpan.duration = nowUs() - pendingPageSpan.startTime
    getClient().enqueue(pendingPageSpan)
    pendingPageSpan = null
  }
}

function startPageTrace(path: string, fromPath?: string) {
  finalizePageSpan()

  pageTraceId = generateTraceId()
  pageSpanId = generateSpanId()

  const attributes: Record<string, string | number | boolean> = {
    'navigation.to': path,
  }
  if (fromPath) attributes['navigation.from'] = fromPath

  pendingPageSpan = {
    traceId: pageTraceId,
    spanId: pageSpanId,
    name: `page ${path}`,
    service: getClient().getService(),
    kind: 'internal',
    status: 'ok',
    startTime: nowUs(),
    duration: 0,
    attributes,
    events: [],
  }
}

export function getPageTraceId(): string {
  return pageTraceId
}

export function getPageSpanId(): string {
  return pageSpanId
}

function instrumentFetchCalls(telemetryEndpoint: string, errorEndpoint?: string) {
  const originalFetch = window.fetch

  const ignoreUrls = [telemetryEndpoint]
  if (errorEndpoint) ignoreUrls.push(errorEndpoint)

  window.fetch = function (input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url

    if (ignoreUrls.some(u => url.includes(u))) {
      return originalFetch.call(this, input, init)
    }

    const method = init?.method ?? 'GET'
    const spanId = generateSpanId()
    const startUs = nowUs()

    const headers = new Headers(init?.headers)
    if (pageTraceId) {
      headers.set('traceparent', makeTraceparent(pageTraceId, spanId))
    }

    return originalFetch.call(this, input, { ...init, headers }).then(
      (response) => {
        getClient().enqueue({
          traceId: pageTraceId,
          spanId,
          parentSpanId: pageSpanId,
          name: `${method} ${new URL(url, location.origin).pathname}`,
          service: getClient().getService(),
          kind: 'client',
          status: response.ok ? 'ok' : 'error',
          startTime: startUs,
          duration: nowUs() - startUs,
          attributes: {
            'http.method': method,
            'http.url': url,
            'http.status_code': response.status,
          },
          events: [],
        })
        return response
      },
      (error) => {
        getClient().enqueue({
          traceId: pageTraceId,
          spanId,
          parentSpanId: pageSpanId,
          name: `${method} ${new URL(url, location.origin).pathname}`,
          service: getClient().getService(),
          kind: 'client',
          status: 'error',
          startTime: startUs,
          duration: nowUs() - startUs,
          attributes: {
            'http.method': method,
            'http.url': url,
            'error.message': error instanceof Error ? error.message : String(error),
          },
          events: [],
        })
        throw error
      },
    )
  }
}

function instrumentSPANavigation() {
  currentPath = location.pathname
  const originalPushState = history.pushState.bind(history)
  const originalReplaceState = history.replaceState.bind(history)

  startPageTrace(currentPath)

  function onNav() {
    const newPath = location.pathname
    if (newPath === currentPath) return
    const fromPath = currentPath
    currentPath = newPath
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
    getClient().enqueue({
      traceId: pageTraceId || generateTraceId(),
      spanId: generateSpanId(),
      parentSpanId: pageSpanId || undefined,
      name: `webvital.${name}`,
      service: getClient().getService(),
      kind: 'internal',
      status: 'ok',
      startTime: nowUs(),
      duration: Math.round(valueMs * 1000),
      attributes: {
        'webvital.name': name,
        'webvital.value_ms': Math.round(valueMs * 100) / 100,
        'webvital.rating': rating,
        'webvital.page': location.pathname,
      },
      events: [],
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
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        if (entry.entryType === 'largest-contentful-paint') {
          reportVital('LCP', entry.startTime, rate('LCP', entry.startTime))
        }
      }
    }).observe({ type: 'largest-contentful-paint', buffered: true })
  } catch { /* not supported */ }

  try {
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        if (entry.name === 'first-contentful-paint') {
          reportVital('FCP', entry.startTime, rate('FCP', entry.startTime))
        }
      }
    }).observe({ type: 'paint', buffered: true })
  } catch { /* not supported */ }

  try {
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        const nav = entry as PerformanceNavigationTiming
        const ttfb = nav.responseStart - nav.requestStart
        if (ttfb > 0) reportVital('TTFB', ttfb, rate('TTFB', ttfb))
      }
    }).observe({ type: 'navigation', buffered: true })
  } catch { /* not supported */ }

  try {
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        if (!(entry as LayoutShiftEntry).hadRecentInput) {
          const value = (entry as LayoutShiftEntry).value
          reportVital('CLS', value * 1000, rate('CLS', value))
        }
      }
    }).observe({ type: 'layout-shift', buffered: true })
  } catch { /* not supported */ }

  try {
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        const dur = entry.duration
        if (dur > 0) reportVital('INP', dur, rate('INP', dur))
      }
    }).observe({ type: 'event', buffered: true, durationThreshold: 16 } as PerformanceObserverInit)
  } catch { /* not supported */ }
}

interface LayoutShiftEntry extends PerformanceEntry {
  hadRecentInput: boolean
  value: number
}

function installGlobalErrorHandlers(errorEndpoint?: string) {
  function reportError(error: Error, attrs?: Record<string, string>) {
    if (errorEndpoint) {
      fetch(errorEndpoint, {
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

    getClient().enqueue({
      traceId: pageTraceId || generateTraceId(),
      spanId: generateSpanId(),
      parentSpanId: pageSpanId || undefined,
      name: `error: ${error.name || 'Error'}`,
      service: getClient().getService(),
      kind: 'internal',
      status: 'error',
      startTime: nowUs(),
      duration: 0,
      attributes: {
        'error.type': error.name || 'Error',
        'error.message': error.message,
        'error.url': location.href,
      },
      events: [],
    })
  }

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

export function instrumentBrowser(config: BrowserInstrumentationConfig = {}) {
  const {
    webVitals = true,
    fetch: instrumentFetch = true,
    navigation = true,
    errors = true,
    errorEndpoint,
  } = config

  const client = getClient()
  const endpoint = client.getConfig().endpoint

  if (navigation) instrumentSPANavigation()
  if (instrumentFetch) instrumentFetchCalls(endpoint, errorEndpoint)
  if (errors) installGlobalErrorHandlers(errorEndpoint)
  if (webVitals) observeWebVitals()

  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') {
      finalizePageSpan()
      void client.flush()
    }
  })
}
