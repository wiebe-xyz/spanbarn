import { createContext, useContext } from 'react'

export type TimeRangeContextType = {
  range: string
  setRange: (range: string) => void
}

export const TimeRangeContext = createContext<TimeRangeContextType>({
  range: '1h',
  setRange: () => {},
})

export function useTimeRange() {
  return useContext(TimeRangeContext)
}
