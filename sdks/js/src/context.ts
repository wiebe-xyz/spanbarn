/** Generate a W3C traceparent header value */
export function makeTraceparent(traceId: string, spanId: string): string {
  return `00-${traceId}-${spanId}-01`
}

/** Parse a W3C traceparent header */
export function parseTraceparent(
  header: string
): { traceId: string; spanId: string } | null {
  const parts = header.split('-')
  if (parts.length !== 4) return null

  const [version, traceId, spanId, flags] = parts
  if (version !== '00') return null
  if (!/^[0-9a-f]{32}$/.test(traceId)) return null
  if (!/^[0-9a-f]{16}$/.test(spanId)) return null
  if (!/^[0-9a-f]{2}$/.test(flags)) return null

  return { traceId, spanId }
}

/** Inject traceparent into a headers object */
export function injectTraceparent(
  headers: Record<string, string>,
  traceId: string,
  spanId: string
): void {
  headers['traceparent'] = makeTraceparent(traceId, spanId)
}
