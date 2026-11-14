/**
 * Describe the part of a time range a truncated result covers: it holds the
 * newest data from coveredFrom up to the end of the range. subject names the
 * view, as in "this map" or "this list".
 */
export function coverageNote(coveredFrom: string, rangeEnd: Date, subject = 'map'): string {
  const minutes = Math.max(1, Math.round((rangeEnd.getTime() - new Date(coveredFrom).getTime()) / 60_000))
  const span = minutes < 120 ? `${minutes} min` : `${Math.round(minutes / 60)} h`
  return `Built from the newest spans only: this ${subject} covers the last ${span} of the selected range.`
}
