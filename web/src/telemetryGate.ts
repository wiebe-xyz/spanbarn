// The telemetry endpoint sits behind a session, so a signed-out page gets a 401
// for every batch. The page also probes the session on load, and that probe
// answers 401 too. Two rules keep a signed-out page quiet:
//   1. A 401 from any API call is an expected answer, not a failed request, so
//      its span carries status OK and does not trigger an error flush.
//   2. After the telemetry endpoint itself answers 401, no more batches go out
//      until a sign-in or a session check succeeds.
// The status code stays on the span as an attribute.

export const TELEMETRY_PATH = '/api/v1/telemetry'
const SESSION_PATHS = ['/api/v1/login', '/api/v1/me']

export function spanStatusFor(httpStatus: number): 'OK' | 'ERROR' {
  if (httpStatus === 401) return 'OK'
  return httpStatus >= 200 && httpStatus < 400 ? 'OK' : 'ERROR'
}

export type TelemetryGate = {
  canSend: () => boolean
  /** Feed every response the page sees; the gate picks out what matters. */
  observe: (path: string, httpStatus: number) => void
}

export function createTelemetryGate(): TelemetryGate {
  let blocked = false
  return {
    canSend: () => !blocked,
    observe(path, httpStatus) {
      if (path === TELEMETRY_PATH && httpStatus === 401) {
        blocked = true
        return
      }
      if (blocked && SESSION_PATHS.includes(path) && httpStatus >= 200 && httpStatus < 300) {
        blocked = false
      }
    },
  }
}
