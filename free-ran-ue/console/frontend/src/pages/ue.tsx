import { Outlet, useNavigate } from 'react-router-dom'
import styles from './css/ue.module.css'
import Sidebar from '../components/sidebar/sidebar'
import { useUe } from '../context/ueContext'

export default function Ue() {
  const navigate = useNavigate()
  const { ranUeList, xnUeList } = useUe()

  const handleGnbClick = (gnbId: string) => {
    navigate(`/gnb/${gnbId}`)
  }

  return (
    <div className={styles.container}>
      <Sidebar />
      <div className={styles.content}>
        <div className={styles.header}>
          <h1>UE</h1>
        </div>

        <div className={styles.infoCard}>
          <h2 className={styles.title}>RAN UE List</h2>
          <div className={styles.ueList}>
            <table className={styles.table}>
              <thead className={styles.tableHeader}>
                <tr>
                  <th>No.</th>
                  <th>UE</th>
                  <th>gNB</th>
                  <th>DC-status</th>
                </tr>
              </thead>
              <tbody>
                {ranUeList.map((ue, index) => (
                  <tr key={ue.imsi}>
                    <td>{index + 1}</td>
                    <td>{ue.imsi}</td>
                    <td>
                      <span 
                        className={styles.gnbLink}
                        onClick={() => handleGnbClick(ue.gnbId)}
                      >
                        {ue.gnbName || ue.gnbId}
                      </span>
                    </td>
                    <td>
                      <span className={ue.nrdcIndicator ? styles.statusOnline : styles.statusOffline}>
                        {ue.nrdcIndicator ? 'Enabled' : 'Disabled'}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>

        <div className={styles.infoCard}>
          <h2 className={styles.title}>XN UE List</h2>
          <div className={styles.ueList}>
            <table className={styles.table}>
              <thead className={styles.tableHeader}>
                <tr>
                  <th>No.</th>
                  <th>UE</th>
                  <th>gNB</th>
                </tr>
              </thead>
              <tbody>
                {xnUeList.map((ue, index) => (
                  <tr key={ue.imsi}>
                    <td>{index + 1}</td>
                    <td>{ue.imsi}</td>
                    <td>
                      <span 
                        className={styles.gnbLink}
                        onClick={() => handleGnbClick(ue.gnbId)}
                      >
                        {ue.gnbName || ue.gnbId}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
        <Outlet />
      </div>
    </div>
  )
}
