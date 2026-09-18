import ErrorBox from '../errorBox/errorBox'
import SuccessBox from '../successBox/successBox'
import { useNotifications } from '../../hooks/useNotifications'

export default function NotificationContainer() {
  const { errors, successes, removeNotification } = useNotifications()

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
        duration={3000}
      />
    </>
  )
}
