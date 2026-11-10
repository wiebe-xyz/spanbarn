import { useEffect, useState, type ReactElement, type ReactNode } from 'react'
import { api } from '../api/client'

type Check = 'pending' | 'ok' | 'signed-out'

/**
 * Holds the dashboard back until the session is confirmed. A signed-out visit
 * sends one /api/v1/me request, and fetchJSON's 401 handling takes it to
 * sign-in before any dashboard or project read goes out. Other failures (a
 * network error, a 5xx) let the page render, so an outage of this one check
 * does not lock signed-in users out.
 */
export function AuthGate({ children }: { children: ReactNode }): ReactElement | null {
  const [check, setCheck] = useState<Check>('pending')

  useEffect(() => {
    let cancelled = false
    api
      .getMe()
      .then(() => !cancelled && setCheck('ok'))
      .catch((err: unknown) => {
        if (cancelled) return
        setCheck((err as { status?: number })?.status === 401 ? 'signed-out' : 'ok')
      })
    return () => {
      cancelled = true
    }
  }, [])

  if (check !== 'ok') return null
  return <>{children}</>
}
