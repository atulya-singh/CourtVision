import { useEffect, useRef, useState } from 'react'

// DecisionStatus mirrors types.DecisionStatus on the Go side. It is the single
// source of truth for where a decision is in its lifecycle, so the UI never has
// to guess from other fields.
type DecisionStatus =
  | 'none'
  | 'pending'
  | 'executing'
  | 'executed'
  | 'failed'
  | 'rejected'

interface Decision {
  id: string
  timestamp: string
  severity: string
  action: string
  target_pod: string
  namespace: string
  target_node?: string
  reasoning: string
  status: DecisionStatus
  executed: boolean
  executed_at?: string
  error?: string
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

const statusStyles: Record<DecisionStatus, { label: string; className: string } | null> = {
  none: null,
  pending: null,
  executing: {
    label: 'EXECUTING',
    className: 'bg-yellow-500/20 text-yellow-400 border-yellow-500/40 animate-pulse',
  },
  executed: {
    label: 'EXECUTED',
    className: 'bg-green-500/20 text-green-400 border-green-500/40',
  },
  failed: {
    label: 'FAILED',
    className: 'bg-red-500/20 text-red-400 border-red-500/40',
  },
  rejected: {
    label: 'REJECTED',
    className: 'bg-gray-500/20 text-gray-400 border-gray-500/40',
  },
}

function StatusBadge({ status }: { status: DecisionStatus }) {
  const style = statusStyles[status]
  if (!style) return null
  return (
    <span className={`text-xs font-semibold px-2 py-0.5 rounded-full border ${style.className}`}>
      {style.label}
    </span>
  )
}

function DecisionCard({ decision, isNew }: { decision: Decision; isNew: boolean }) {
  const time = new Date(decision.timestamp).toLocaleTimeString()
  const status = decision.status

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
      {status === 'failed' && decision.error && (
        <p className="text-xs text-red-400 mb-3">Execution failed: {decision.error}</p>
      )}
      {status === 'pending' && (
        <p className="text-xs text-gray-500 italic">
          Awaiting approval in the CLI (analyze --apply / review) or auto-safe.
        </p>
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

    // The server sends a "decision" event both for brand-new decisions and for
    // state changes to existing ones (pending -> executing -> executed). So we
    // upsert by id: replace the card if we already have it, otherwise prepend.
    es.addEventListener('decision', (e) => {
      const decision: Decision = JSON.parse(e.data)
      setDecisions((prev) => {
        const idx = prev.findIndex((d) => d.id === decision.id)
        if (idx >= 0) {
          const next = prev.slice()
          next[idx] = decision
          return next
        }
        newIdsRef.current.add(decision.id)
        setTimeout(() => newIdsRef.current.delete(decision.id), 600)
        return [decision, ...prev]
      })
    })

    es.onerror = () => setConnected(false)
    es.onopen = () => setConnected(true)

    return () => es.close()
  }, [])

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
            />
          ))
        )}
      </div>
    </div>
  )
}
