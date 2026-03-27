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

function DecisionCard({ decision, isNew }: { decision: Decision; isNew: boolean }) {
  const time = new Date(decision.timestamp).toLocaleTimeString()

  return (
    <div
      className={`rounded-xl p-4 backdrop-blur-md bg-white/5 border border-white/10 transition-all duration-500 ${
        isNew ? 'animate-slide-in' : ''
      }`}
    >
      <div className="flex items-center justify-between mb-2">
        <SeverityBadge severity={decision.severity} />
        <span className="text-xs text-gray-500">{time}</span>
      </div>
      <div className="mb-2">
        <span className="text-white font-medium">{decision.target_pod}</span>
        <span className="text-gray-500 text-sm ml-2">{decision.action.replace(/_/g, ' ')}</span>
      </div>
      <p className="text-sm text-gray-400 leading-relaxed">{decision.reasoning}</p>
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
      // Remove from "new" set after animation completes
      setTimeout(() => newIdsRef.current.delete(decision.id), 600)
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
            <DecisionCard key={d.id} decision={d} isNew={newIdsRef.current.has(d.id)} />
          ))
        )}
      </div>
    </div>
  )
}
