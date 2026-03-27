import { useEffect, useRef, useState } from 'react'

interface Decision {
  id: string
  timestamp: string
  severity: string
  action: string
  target_pod: string
  namespace: string
  target_node?: string
  reasoning: string
  executed: boolean
  executed_at?: string
  error?: string
}

type DecisionStatus = 'pending' | 'approved' | 'rejected'

function getStatus(d: Decision): DecisionStatus {
  if (d.executed_at && d.error === 'rejected by operator') return 'rejected'
  if (d.executed_at && d.executed) return 'approved'
  if (d.executed) return 'approved'
  return 'pending'
}

const severityStyles: Record<string, string> = {
  critical: 'bg-red-500/20 text-red-400 border-red-500/40',
  high: 'bg-orange-500/20 text-orange-400 border-orange-500/40',
  medium: 'bg-yellow-500/20 text-yellow-400 border-yellow-500/40',
  low: 'bg-blue-500/20 text-blue-400 border-blue-500/40',
}

function SeverityBadge({ severity }: { severity: string }) {
  return (
    <span
      className={`text-xs font-semibold px-2 py-0.5 rounded-full border ${
        severityStyles[severity] || severityStyles.low
      }`}
    >
      {severity.toUpperCase()}
    </span>
  )
}

function StatusBadge({ status }: { status: DecisionStatus }) {
  if (status === 'approved') {
    return (
      <span className="text-xs font-semibold px-2 py-0.5 rounded-full bg-green-500/20 text-green-400 border border-green-500/40">
        APPROVED
      </span>
    )
  }
  if (status === 'rejected') {
    return (
      <span className="text-xs font-semibold px-2 py-0.5 rounded-full bg-red-500/20 text-red-400 border border-red-500/40">
        REJECTED
      </span>
    )
  }
  return null
}

function DecisionCard({
  decision,
  isNew,
  onAction,
}: {
  decision: Decision
  isNew: boolean
  onAction: (id: string, action: 'approve' | 'reject') => void
}) {
  const time = new Date(decision.timestamp).toLocaleTimeString()
  const status = getStatus(decision)

  return (
    <div
      className={`rounded-xl p-4 backdrop-blur-md bg-white/5 border border-white/10 transition-all duration-500 ${
        isNew ? 'animate-slide-in' : ''
      }`}
    >
      <div className="flex items-center justify-between mb-2">
        <div className="flex items-center gap-2">
          <SeverityBadge severity={decision.severity} />
          <StatusBadge status={status} />
        </div>
        <span className="text-xs text-gray-500">{time}</span>
      </div>
      <div className="mb-2">
        <span className="text-white font-medium">{decision.target_pod}</span>
        <span className="text-gray-500 text-sm ml-2">{decision.action.replace(/_/g, ' ')}</span>
      </div>
      <p className="text-sm text-gray-400 leading-relaxed mb-3">{decision.reasoning}</p>
      {status === 'pending' && (
        <div className="flex gap-2">
          <button
            onClick={() => onAction(decision.id, 'approve')}
            className="px-3 py-1 text-xs font-medium rounded-lg bg-green-500/20 text-green-400 border border-green-500/30 hover:bg-green-500/30 transition-colors cursor-pointer"
          >
            Approve
          </button>
          <button
            onClick={() => onAction(decision.id, 'reject')}
            className="px-3 py-1 text-xs font-medium rounded-lg bg-red-500/20 text-red-400 border border-red-500/30 hover:bg-red-500/30 transition-colors cursor-pointer"
          >
            Reject
          </button>
        </div>
      )}
    </div>
  )
}

export default function DecisionFeed() {
  const [decisions, setDecisions] = useState<Decision[]>([])
  const [connected, setConnected] = useState(false)
  const newIdsRef = useRef<Set<string>>(new Set())

  // Fetch historical decisions on mount
  useEffect(() => {
    fetch('/api/decisions')
      .then((r) => r.json())
      .then((data: Decision[]) => {
        if (Array.isArray(data)) {
          setDecisions(data.slice().reverse())
        }
      })
      .catch(console.error)
  }, [])

  // SSE for real-time decisions
  useEffect(() => {
    const es = new EventSource('/api/events')

    es.addEventListener('connected', () => setConnected(true))

    es.addEventListener('decision', (e) => {
      const decision: Decision = JSON.parse(e.data)
      newIdsRef.current.add(decision.id)
      setDecisions((prev) => [decision, ...prev])
      setTimeout(() => newIdsRef.current.delete(decision.id), 600)
    })

    es.onerror = () => setConnected(false)
    es.onopen = () => setConnected(true)

    return () => es.close()
  }, [])

  const handleAction = (id: string, action: 'approve' | 'reject') => {
    fetch(`/api/decisions/${id}/${action}`, { method: 'POST' })
      .then((r) => {
        if (!r.ok) throw new Error('Failed')
        setDecisions((prev) =>
          prev.map((d) => {
            if (d.id !== id) return d
            const now = new Date().toISOString()
            if (action === 'approve') {
              return { ...d, executed: true, executed_at: now }
            }
            return { ...d, executed: false, executed_at: now, error: 'rejected by operator' }
          })
        )
      })
      .catch(console.error)
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h2 className="text-lg font-semibold text-white">Decision Feed</h2>
        <div className="flex items-center gap-2">
          <div
            className={`w-2 h-2 rounded-full ${connected ? 'bg-green-500' : 'bg-red-500'}`}
          />
          <span className="text-xs text-gray-400">
            {connected ? 'Connected' : 'Disconnected'}
          </span>
        </div>
      </div>
      <div className="space-y-3 max-h-[calc(100vh-12rem)] overflow-y-auto pr-1">
        {decisions.length === 0 ? (
          <div className="text-gray-500 text-center py-8">No decisions yet</div>
        ) : (
          decisions.map((d) => (
            <DecisionCard
              key={d.id}
              decision={d}
              isNew={newIdsRef.current.has(d.id)}
              onAction={handleAction}
            />
          ))
        )}
      </div>
    </div>
  )
}
