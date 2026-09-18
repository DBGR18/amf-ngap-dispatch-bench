import { createContext, useContext, useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { useGnb } from './gnbContext'

interface RanUe {
  imsi: string
  nrdcIndicator: boolean
  gnbId: string
  gnbName: string
}

interface XnUe {
  imsi: string
  gnbId: string
  gnbName: string
}

interface UeContextType {
  ranUeList: RanUe[]
  xnUeList: XnUe[]
  totalRanUes: number
  totalXnUes: number
}

const UeContext = createContext<UeContextType>({
  ranUeList: [],
  xnUeList: [],
  totalRanUes: 0,
  totalXnUes: 0
})

export function UeProvider({ children }: { children: ReactNode }) {
  const { gnbList } = useGnb()
  const [ranUeList, setRanUeList] = useState<RanUe[]>([])
  const [xnUeList, setXnUeList] = useState<XnUe[]>([])

  useEffect(() => {
    const newRanUeList = gnbList.reduce<RanUe[]>((acc, gnb) => {
      if (!gnb.gnbInfo?.ranUeList) return acc
      
      const ueList = gnb.gnbInfo.ranUeList.map(ue => ({
        imsi: ue.imsi || '',
        nrdcIndicator: ue.nrdcIndicator || false,
        gnbId: gnb.gnbInfo?.gnbId || '',
        gnbName: gnb.gnbInfo?.gnbName || ''
      }))
      return [...acc, ...ueList]
    }, [])
    setRanUeList(newRanUeList)

    const newXnUeList = gnbList.reduce<XnUe[]>((acc, gnb) => {
      if (!gnb.gnbInfo?.xnUeList) return acc
      
      const ueList = gnb.gnbInfo.xnUeList.map(ue => ({
        imsi: ue.imsi || '',
        gnbId: gnb.gnbInfo?.gnbId || '',
        gnbName: gnb.gnbInfo?.gnbName || ''
      }))
      return [...acc, ...ueList]
    }, [])
    setXnUeList(newXnUeList)
  }, [gnbList])

  const value = {
    ranUeList,
    xnUeList,
    totalRanUes: ranUeList.length,
    totalXnUes: xnUeList.length
  }

  return <UeContext.Provider value={value}>{children}</UeContext.Provider>
}

export function useUe() {
  const context = useContext(UeContext)
  if (context === undefined) {
    throw new Error('useUe must be used within a UeProvider')
  }
  return context
}
