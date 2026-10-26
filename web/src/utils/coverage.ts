/**
 * Describe the part of a time range a truncated result covers: it holds the
 * newest data from coveredFrom up to the end of the range.
 */
export function coverageNote(coveredFrom: string, rangeEnd: Date): string {
  const minutes = Math.max(1, Math.round((rangeEnd.getTime() - new Date(coveredFrom).getTime()) / 60_000))
  const span = minutes < 120 ? `${minutes} min` : `${Math.round(minutes / 60)} h`
  return `Built from the newest spans only: this map covers the last ${span} of the selected range.`
}
