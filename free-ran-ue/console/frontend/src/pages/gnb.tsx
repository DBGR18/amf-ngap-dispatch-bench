import { Outlet, useNavigate } from 'react-router-dom'
import { useState } from 'react'
import styles from './css/gnb.module.css'
import Sidebar from '../components/sidebar/sidebar'
import Button from '../components/button/button'
import AddGnbModal from '../components/gnb/add-gnb-modal'
import { useGnb } from '../context/gnbContext'
import { gnbApi } from '../apiCfg'
import { useNotifications } from '../hooks/useNotifications'
import ErrorBox from '../components/errorBox/errorBox'
import SuccessBox from '../components/successBox/successBox'

export default function Gnb() {
  const [isAddModalOpen, setIsAddModalOpen] = useState(false)
  const { gnbList, removeGnb, addGnb, setGnbStatus } = useGnb()
  const { errors, successes, addError, addSuccess, removeNotification } = useNotifications()
  const navigate = useNavigate()

  const handleAddGnb = async () => {
    setIsAddModalOpen(true)
  }

  const handleUpdateGnb = async () => {
    let hasError = false
    for (const gnb of gnbList) {
      try {
        const result = await gnbApi.apiConsoleGnbInfoPost({
          ip: gnb.connection?.ip || '',
          port: gnb.connection?.port || 0
        }, {
          headers: {
            'Authorization': localStorage.getItem('token')
          }
        })
        addGnb(result.data, gnb.connection || { ip: '', port: 0 })
      } catch (error: any) {
        hasError = true
        addError("gNB " + gnb.gnbInfo?.gnbName + " update failed: " + error.response?.data?.message)
        if (error.response?.status === 401) {
          setTimeout(() => {
            navigate('/login')
          }, 2000)
          return
        } else {
          setGnbStatus(gnb.gnbInfo?.gnbId || '', 'offline')
        }
      }
    }

    if (!hasError) {
      addSuccess('gNB updated successfully')
    }
  }

  return (
    <>
      <ErrorBox 
        errors={errors}
        onClose={removeNotification}
        duration={5000}
      />
      <SuccessBox 
        successes={successes}
        onClose={removeNotification}
        duration={5000}
      />
      <div className={styles.container}>
        <Sidebar />
        <div className={styles.content}>
          <div className={styles.header}>
            <h1>gNB</h1>
            <div className={styles.headerButtons}>
              <Button onClick={handleUpdateGnb}>Refresh</Button>
              <Button onClick={handleAddGnb}>Add gNB</Button>
            </div>
          </div>

          <div className={styles.list}>
            <table className={styles.table}>
              <thead className={styles.tableHeader}>
                <tr>
                  <th>No.</th>
                  <th>Status</th>
                  <th>gNB Name</th>
                  <th>Info</th>
                  <th>Action</th>
                </tr>
              </thead>
              <tbody className={styles.tableBody}>
                {gnbList.map((gnb, index) => (
                  <tr key={gnb.gnbInfo?.gnbId}>
                    <td>{index + 1}</td>
                    <td>
                    <span className={`${styles.status} ${gnb.status === 'online' ? styles.statusOnline : styles.statusOffline}`}>
                      {gnb.status === 'online' ? 'Online' : 'Offline'}
                    </span>
                    </td>
                    <td>{gnb.gnbInfo?.gnbName}</td>
                    <td>
                      <button 
                        className={`${styles.actionButton} ${styles.infoButton}`}
                        onClick={() => navigate(`/gnb/${gnb.gnbInfo?.gnbId}`)}
                      >
                        View Info
                      </button>
                    </td>
                    <td>
                      <button 
                        className={`${styles.actionButton} ${styles.removeButton}`}
                        onClick={() => removeGnb(gnb.gnbInfo?.gnbId || '')}
                      >
                        Remove
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <Outlet />
        </div>
        <AddGnbModal 
          isOpen={isAddModalOpen}
          onClose={() => setIsAddModalOpen(false)}
        />
      </div>
    </>
  )
}
