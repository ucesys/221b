import { useEffect } from 'react'

export function Toast({ message, onDone }: { message: string; onDone: () => void }) {
  useEffect(() => {
    const t = setTimeout(onDone, 4000)
    return () => clearTimeout(t)
  }, [message, onDone])
  return (
    <div className="rk-toast" role="status" aria-live="polite">
      <span>{message}</span>
      <button onClick={onDone} aria-label="Dismiss">
        OK
      </button>
    </div>
  )
}
