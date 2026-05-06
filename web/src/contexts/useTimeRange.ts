import { useContext } from 'react'
import { TimeRangeContext } from './TimeRangeContext'

export function useTimeRange() {
  return useContext(TimeRangeContext)
}
