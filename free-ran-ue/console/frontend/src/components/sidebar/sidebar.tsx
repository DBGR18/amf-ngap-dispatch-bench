import { NavLink, useNavigate } from 'react-router-dom'
import styles from './sidebar.module.css'
import { consoleApi } from '../../apiCfg'

export default function Sidebar() {
  const navigate = useNavigate()
  const navItems = [
    { path: '/dashboard', label: 'Dashboard' },
    { path: '/gnb', label: 'gNB' },
    { path: '/ue', label: 'UE' },
  ]
  
  const handleLogout = async () => {
    try {
      await consoleApi.apiConsoleLogoutDelete()
    } finally {
      localStorage.removeItem('token')
      localStorage.removeItem('gnbList')
      navigate('/login')
    }
  }

  return (
    <aside className={styles.sidebar}>
      <div className={styles.logo}>
        <h1>free-ran-ue</h1>
      </div>
      <nav className={styles.nav}>
        {navItems.map(item => (
          <NavLink
            key={item.path}
            to={item.path}
            className={({ isActive }) => 
              `${styles.navItem} ${isActive ? styles.active : ''}`
            }
          >
            {item.label}
          </NavLink>
        ))}
      </nav>
      <div className={styles.footer}>
        <button className={styles.logoutButton} onClick={handleLogout}>Logout</button>
      </div>
    </aside>
  )
}
