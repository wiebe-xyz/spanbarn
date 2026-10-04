export type SloFormValues = { name: string; targetPercent: string; windowDays: string }

/** The first problem with the SLO form values, or null when they can be sent. */
export function validateSlo(v: SloFormValues): string | null {
  if (!v.name.trim()) return 'Name is required'
  if (v.name.trim().length > 120) return 'Name is limited to 120 characters'
  const target = Number(v.targetPercent)
  if (!v.targetPercent.trim() || !(target > 0 && target < 100)) return 'Target must be above 0 and below 100 percent'
  const days = Number(v.windowDays)
  if (!Number.isInteger(days) || days < 1 || days > 90) return 'Window must be a whole number of days from 1 to 90'
  return null
}

export type BurnAlertValues = { windowMinutes: string; burnRate: string; email: string; webhookUrl: string; cooldownMinutes: string }

/** The first problem with the burn alert values, or null when they can be sent. */
export function validateBurnAlert(v: BurnAlertValues, windowDays: number): string | null {
  const minutes = Number(v.windowMinutes)
  const limit = windowDays * 24 * 60
  if (!Number.isInteger(minutes) || minutes <= 0) return 'Alert window must be a whole number of minutes above 0'
  if (minutes >= limit) return `Alert window must be shorter than the SLO window of ${limit} minutes`
  if (!(Number(v.burnRate) > 0)) return 'Burn rate must be above 0'
  const cooldown = Number(v.cooldownMinutes || 0)
  if (!Number.isInteger(cooldown) || cooldown < 0) return 'Cooldown must be a whole number of minutes, 0 or more'
  if (v.email.trim() && !/^[^\s@]+@[^\s@]+$/.test(v.email.trim())) return 'Email is not a valid address'
  if (v.webhookUrl.trim() && !/^https?:\/\//i.test(v.webhookUrl.trim())) return 'Webhook URL must start with http:// or https://'
  return null
}

/** A burn alert window as days, hours or minutes. */
export function formatWindow(minutes: number): string {
  if (minutes % 1440 === 0) return `${minutes / 1440}d`
  if (minutes % 60 === 0) return `${minutes / 60}h`
  return `${minutes}m`
}
