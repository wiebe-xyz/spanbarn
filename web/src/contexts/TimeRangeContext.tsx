import { createContext, useState, type ReactNode } from 'react'

export type TimeRangeContextType = {
  range: string
  setRange: (range: string) => void
}

export const TimeRangeContext = createContext<TimeRangeContextType>({
  range: '1h',
  setRange: () => {},
})

export function TimeRangeProvider({ children }: { children: ReactNode }) {
  const [range, setRange] = useState('1h')
  return (
    <TimeRangeContext.Provider value={{ range, setRange }}>
      {children}
    </TimeRangeContext.Provider>
  )
}
