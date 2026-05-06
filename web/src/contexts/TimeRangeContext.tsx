import { createContext, useContext, useState, type ReactNode } from 'react'

type TimeRangeContextType = {
  range: string
  setRange: (range: string) => void
}

const TimeRangeContext = createContext<TimeRangeContextType>({
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

export function useTimeRange() {
  return useContext(TimeRangeContext)
}
