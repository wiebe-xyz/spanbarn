import { useState, type ReactNode } from 'react'
import { TimeRangeContext } from './useTimeRange'

export function TimeRangeProvider({ children }: { children: ReactNode }) {
  const [range, setRange] = useState('1h')
  return (
    <TimeRangeContext.Provider value={{ range, setRange }}>
      {children}
    </TimeRangeContext.Provider>
  )
}
