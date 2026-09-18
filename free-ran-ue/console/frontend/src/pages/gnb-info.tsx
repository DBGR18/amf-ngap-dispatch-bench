import { useParams, useNavigate } from 'react-router-dom'
import styles from './css/gnb-info.module.css'
import { useGnb } from '../context/gnbContext'
import Button from '../components/button/button'
import Sidebar from '../components/sidebar/sidebar'
import Switch from '../components/switch/switch'
import { gnbApi } from '../apiCfg'
import { useNotifications } from '../hooks/useNotifications'
import ErrorBox from '../components/errorBox/errorBox'
import SuccessBox from '../components/successBox/successBox'

export default function GnbInfo() {
  const { gnbId } = useParams()
  const navigate = useNavigate()
  const { gnbList, updateUeNrdcIndicator, addGnb, setGnbStatus } = useGnb()
  const { successes, errors, addSuccess, addError, removeNotification } = useNotifications()

  const gnb = gnbList.find(gnb => gnb.gnbInfo?.gnbId === gnbId)

  const updateGnb = async () => {
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
  }

  const handleChangeNrdcIndicator = async (gnbId: string, ip: string, port: number, imsi: string, indicator: boolean) => {
    try {
      const response = await gnbApi.apiConsoleGnbUeNrdcPost({
        ip: ip,
        port: port,
        imsi: imsi
      }, {
        headers: {
          'Authorization': localStorage.getItem('token')
        }
      })

      updateUeNrdcIndicator(gnbId, imsi, !indicator)
      updateGnb()

      addSuccess(response.data.message || 'UE DC-status changed successfully')
    } catch (error: any) {
      addError(error.response?.data?.message || 'Failed to change UE DC-status')
      if (error.response?.status === 401) {
        setTimeout(() => {
          navigate('/login')
        }, 2000)
      }
    }
  }

  if (!gnb) {
    return (
      <div className={styles.container}>
        <Sidebar />
        <div className={styles.content}>
          <div className={styles.header}>
            <Button variant="secondary" onClick={() => navigate('/gnb')}>
              Back to List
            </Button>
          </div>
          <div className={styles.error}>
            gNB not found
          </div>
        </div>
      </div>
    )
  }

  return (
    <>
      <SuccessBox
        successes={successes}
        onClose={removeNotification}
        duration={5000}
      />
      <ErrorBox 
        errors={errors}
        onClose={removeNotification}
        duration={5000}
      />
      <div className={styles.container}>
        <Sidebar />
        <div className={styles.content}>
          <div className={styles.header}>
            <Button variant="secondary" onClick={() => navigate('/gnb')}>
              Back to List
            </Button>
          </div>

          <div className={styles.infoCard}>
            <h2 className={styles.title}>gNB Information</h2>
            
            <div className={styles.infoGroup}>
              <label>gNB ID</label>
              <div>{gnb.gnbInfo?.gnbId}</div>
            </div>

            <div className={styles.infoGroup}>
              <label>gNB Name</label>
              <div>{gnb.gnbInfo?.gnbName}</div>
            </div>

            <div className={styles.infoGroup}>
              <label>PLMN ID</label>
              <div>{gnb.gnbInfo?.plmnId}</div>
            </div>

            <div className={styles.infoGroup}>
              <label>SNSSAI</label>
              <div>
                <div>SST: {gnb.gnbInfo?.snssai?.sst || 'N/A'}</div>
                <div>SD: {gnb.gnbInfo?.snssai?.sd || 'N/A'}</div>
              </div>
            </div>
          </div>

          <div className={styles.infoCard}>
            <h2 className={styles.title}>RAN UE List</h2>
            <div className={styles.ueList}>
              <table className={styles.table}>
                <thead>
                  <tr>
                    <th>No.</th>
                    <th>UE</th>
                    <th>DC-status</th>
                  </tr>
                </thead>
                <tbody>
                  {gnb.gnbInfo?.ranUeList?.map((ue, index) => (
                    <tr key={ue.imsi}>
                      <td>{index + 1}</td>
                      <td>{ue.imsi}</td>
                      <td>
                        <Switch
                          checked={ue.nrdcIndicator || false}
                          onChange={() => {
                            handleChangeNrdcIndicator(gnb.gnbInfo?.gnbId!, gnb.connection?.ip!, gnb.connection?.port!, ue.imsi!, ue.nrdcIndicator!)
                          }}
                        />
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
                <thead>
                  <tr>
                    <th>No.</th>
                    <th>UE</th>
                  </tr>
                </thead>
                <tbody>
                  {gnb.gnbInfo?.xnUeList?.map((ue, index) => (
                    <tr key={ue.imsi}>
                      <td>{index + 1}</td>
                      <td>{ue.imsi}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </div>
      </div>
    </>
  )
}
