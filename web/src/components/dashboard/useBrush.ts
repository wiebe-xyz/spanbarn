import { useCallback, useRef, useState } from 'react'

/** A position on a chart: x in epoch ms, y optional (microseconds on a heatmap). */
export type BrushPoint = { x: number; y?: number }
export type BrushRect = { start: BrushPoint; end: BrushPoint }

/**
 * Drag-selection state shared by the line charts and the heatmaps. begin/extend/
 * finish map to mouse down/move/up; a drag that never moved commits nothing.
 */
export function useBrush(onCommit: (rect: BrushRect) => void) {
  const [rect, setRect] = useState<BrushRect | null>(null)
  const live = useRef<BrushRect | null>(null)

  const set = useCallback((next: BrushRect | null) => {
    live.current = next
    setRect(next)
  }, [])

  const begin = useCallback((p: BrushPoint) => set({ start: p, end: p }), [set])
  const extend = useCallback(
    (p: BrushPoint) => {
      if (live.current) set({ start: live.current.start, end: p })
    },
    [set],
  )
  const finish = useCallback(() => {
    const done = live.current
    set(null)
    if (done && done.start.x !== done.end.x) onCommit(done)
  }, [onCommit, set])
  const cancel = useCallback(() => set(null), [set])

  return { rect, begin, extend, finish, cancel }
}

/** The ordered x range of a selection. */
export function xRange(rect: BrushRect): [number, number] {
  return rect.start.x <= rect.end.x ? [rect.start.x, rect.end.x] : [rect.end.x, rect.start.x]
}
