import { Outlet, useNavigate } from 'react-router-dom'
import styles from './css/dashboard.module.css'
import Sidebar from '../components/sidebar/sidebar'
import StatsCard from '../components/stats/stats-card'
import { useGnb } from '../context/gnbContext'
import { useUe } from '../context/ueContext'

export default function Dashboard() {
  const navigate = useNavigate()
  const { gnbList } = useGnb()
  const { totalRanUes, totalXnUes } = useUe()

  return (
    <div className={styles.container}>
      <Sidebar />
      <div className={styles.content}>
        <div className={styles.header}>
          <h1>Dashboard</h1>
        </div>

        <div className={styles.statsSection}>
          <div className={styles.statsRow}>
            <div className={styles.statsCard} onClick={() => navigate('/gnb')}>
              <StatsCard 
                title="Total gNBs"
                value={gnbList.length}
                description="Click to view all gNBs"
              />
            </div>
          </div>
          <div className={styles.statsRow}>
            <div className={styles.statsCard} onClick={() => navigate('/ue')}>
              <StatsCard 
                title="Total RAN UEs"
                value={totalRanUes}
                description="Click to view all UEs"
              />
            </div>
            <div className={styles.statsCard} onClick={() => navigate('/ue')}>
              <StatsCard 
                title="Total XN UEs"
                value={totalXnUes}
                description="Click to view all UEs"
              />
            </div>
          </div>
        </div>

        <Outlet />
      </div>
    </div>
  )
}
