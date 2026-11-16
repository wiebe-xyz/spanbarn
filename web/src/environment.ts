import { useSyncExternalStore } from 'react'

/** The environment filter the web app sends with its reads (#558). */
const STORAGE_KEY = 'spanbarn.environment'
const DEFAULT_ENVIRONMENT = 'production'
export const ALL_ENVIRONMENTS = 'all'

/** The read endpoints that take the `environment` parameter. */
const ENVIRONMENT_PATHS = new Set([
  '/api/v1/traces',
  '/api/v1/spans/search',
  '/api/v1/analyze',
  '/api/v1/analyze/series',
  '/api/v1/heatmap',
  '/api/v1/attributes/compare',
])

function readStored(): string {
  try {
    return localStorage.getItem(STORAGE_KEY) || DEFAULT_ENVIRONMENT
  } catch {
    return DEFAULT_ENVIRONMENT
  }
}

let current = readStored()
const listeners = new Set<() => void>()

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

function snapshot(): string {
  return current
}

/** Sets the environment, remembers it for the next visit and tells every subscriber. */
export function setEnvironment(env: string): void {
  if (env === current) return
  current = env
  try {
    localStorage.setItem(STORAGE_KEY, env)
  } catch {
    // Storage can be blocked; the choice then lasts for this page load.
  }
  listeners.forEach((l) => l())
}

/** The selected environment: a name, or ALL_ENVIRONMENTS. Production until the user picks one. */
export function useEnvironment(): string {
  return useSyncExternalStore(subscribe, snapshot)
}

/**
 * Adds the selected environment to a read URL that takes one, unless the URL
 * names an environment already or the selection is "all".
 */
export function withEnvironment(url: string): string {
  const at = url.indexOf('?')
  const path = at < 0 ? url : url.slice(0, at)
  const query = at < 0 ? '' : url.slice(at + 1)
  if (!ENVIRONMENT_PATHS.has(path) || current === ALL_ENVIRONMENTS) return url
  if (query.split('&').some((p) => p.startsWith('environment='))) return url
  return `${url}${query ? '&' : '?'}environment=${encodeURIComponent(current)}`
}
