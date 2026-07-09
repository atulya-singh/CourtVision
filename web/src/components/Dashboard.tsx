import { useEffect, useState } from 'react'
import ClusterOverview from './ClusterOverview'
import DecisionFeed from './DecisionFeed'
import { endpointsFor, type ClusterSummary, type Scope } from '../scope'

function StatsBar({ decisionsUrl }: { decisionsUrl: string }) {
  const [stats, setStats] = useState({ total: 0, critical: 0 })
  const [uptime, setUptime] = useState(0)

  useEffect(() => {
    let active = true
    const fetchStats = () => {
      fetch(decisionsUrl)
        .then((r) => r.json())
        .then((decisions: { severity: string }[]) => {
          if (active && Array.isArray(decisions)) {
            setStats({
              total: decisions.length,
              critical: decisions.filter((d) => d.severity === 'critical').length,
            })
          }
        })
        .catch(console.error)
    }
    fetchStats()
    const interval = setInterval(fetchStats, 5000)
    return () => {
      active = false
      clearInterval(interval)
    }
  }, [decisionsUrl])

  useEffect(() => {
    const start = Date.now()
    const tick = setInterval(() => setUptime(Math.floor((Date.now() - start) / 1000)), 1000)
    return () => clearInterval(tick)
  }, [])

  const formatUptime = (s: number) => {
    const h = Math.floor(s / 3600)
    const m = Math.floor((s % 3600) / 60)
    const sec = s % 60
    return `${h.toString().padStart(2, '0')}:${m.toString().padStart(2, '0')}:${sec.toString().padStart(2, '0')}`
  }

  return (
    <div className="flex gap-6 text-sm">
      <div className="flex items-center gap-2 px-4 py-2 rounded-lg bg-white/5 backdrop-blur-md border border-white/10">
        <span className="text-gray-400">Decisions</span>
        <span className="text-white font-semibold">{stats.total}</span>
      </div>
      <div className="flex items-center gap-2 px-4 py-2 rounded-lg bg-white/5 backdrop-blur-md border border-white/10">
        <span className="text-gray-400">Critical</span>
        <span className={`font-semibold ${stats.critical > 0 ? 'text-red-400' : 'text-white'}`}>
          {stats.critical}
        </span>
      </div>
      <div className="flex items-center gap-2 px-4 py-2 rounded-lg bg-white/5 backdrop-blur-md border border-white/10">
        <span className="text-gray-400">Uptime</span>
        <span className="text-white font-mono font-semibold">{formatUptime(uptime)}</span>
      </div>
    </div>
  )
}

// ClusterTabs is the multi-cluster scope switcher: a Fleet tab (coordinator
// cross-cluster view) plus one tab per worker, each showing its live node/pod
// counts from the /api/clusters roll-up.
function ClusterTabs({
  clusters,
  scope,
  onSelect,
}: {
  clusters: ClusterSummary[]
  scope: Scope
  onSelect: (s: Scope) => void
}) {
  const tabClass = (active: boolean) =>
    `px-4 py-2 rounded-lg text-sm font-medium border transition-colors ${
      active
        ? 'bg-white/10 border-white/20 text-white'
        : 'bg-white/5 border-white/10 text-gray-400 hover:text-white'
    }`

  return (
    <div className="flex gap-2 mb-6 flex-wrap">
      <button className={tabClass(scope.kind === 'fleet')} onClick={() => onSelect({ kind: 'fleet' })}>
        Fleet
      </button>
      {clusters.map((c) => (
        <button
          key={c.name}
          className={tabClass(scope.kind === 'cluster' && scope.name === c.name)}
          onClick={() => onSelect({ kind: 'cluster', name: c.name })}
        >
          {c.name}
          <span className="ml-2 text-xs text-gray-500">
            {c.node_count}n · {c.pod_count}p
          </span>
        </button>
      ))}
    </div>
  )
}

// FleetSummary is the overview shown for the Fleet scope: a roll-up of every
// cluster with its counts. Clicking a card drills into that cluster.
function FleetSummary({
  clusters,
  onSelect,
}: {
  clusters: ClusterSummary[]
  onSelect: (name: string) => void
}) {
  return (
    <div>
      <h2 className="text-lg font-semibold text-white mb-4">Fleet Overview</h2>
      {clusters.length === 0 ? (
        <div className="flex items-center justify-center h-64 text-gray-500">No clusters yet</div>
      ) : (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
          {clusters.map((c) => (
            <button
              key={c.name}
              onClick={() => onSelect(c.name)}
              className="text-left rounded-xl p-4 backdrop-blur-md bg-white/5 border border-white/10 hover:border-white/30 transition-colors"
            >
              <h3 className="text-white font-semibold mb-2">{c.name}</h3>
              <div className="flex gap-4 text-sm text-gray-400">
                <span>{c.node_count} nodes</span>
                <span>{c.pod_count} pods</span>
                <span>{c.decision_count} decisions</span>
              </div>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

export default function Dashboard() {
  // multi is null until the first probe resolves; clusters holds the /api/clusters
  // roll-up (also polled for live tab counts). scope drives which data the views
  // fetch.
  const [multi, setMulti] = useState<boolean | null>(null)
  const [clusters, setClusters] = useState<ClusterSummary[]>([])
  const [scope, setScope] = useState<Scope>({ kind: 'single' })

  // Probe /api/clusters: present (200) means multi-cluster mode; 404 means the
  // server is single-cluster and only the classic /api/* routes exist. Poll so
  // the tab counts stay live.
  useEffect(() => {
    let active = true
    const probe = () => {
      fetch('/api/clusters')
        .then((r) => (r.ok ? r.json() : null))
        .then((data) => {
          if (!active) return
          if (Array.isArray(data)) {
            setMulti(true)
            setClusters(data)
            // First time we learn it's multi-cluster, default to the fleet view.
            setScope((s) => (s.kind === 'single' ? { kind: 'fleet' } : s))
          } else {
            setMulti(false)
          }
        })
        .catch(() => {
          if (active) setMulti(false)
        })
    }
    probe()
    const interval = setInterval(probe, 5000)
    return () => {
      active = false
      clearInterval(interval)
    }
  }, [])

  const ep = endpointsFor(scope)

  return (
    <div className="min-h-screen bg-gradient-to-br from-gray-950 via-gray-900 to-gray-950 text-white">
      <header className="border-b border-white/10 backdrop-blur-md bg-white/5">
        <div className="max-w-[1600px] mx-auto px-6 py-4 flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-bold tracking-tight">CourtVision</h1>
            <p className="text-sm text-gray-400">Agentic Infrastructure Monitor</p>
          </div>
          <StatsBar key={ep.decisions} decisionsUrl={ep.decisions} />
        </div>
      </header>
      <main className="max-w-[1600px] mx-auto px-6 py-6">
        {multi && (
          <ClusterTabs clusters={clusters} scope={scope} onSelect={setScope} />
        )}
        {/* Keying the scoped views by their data URL remounts them on a scope
            switch, so each starts from a clean loading state instead of briefly
            showing the previous cluster's data. */}
        <div className="flex gap-6">
          <div className="flex-[6] min-w-0">
            {scope.kind === 'fleet' ? (
              <FleetSummary
                clusters={clusters}
                onSelect={(name) => setScope({ kind: 'cluster', name })}
              />
            ) : (
              <ClusterOverview key={ep.snapshot} snapshotUrl={ep.snapshot} />
            )}
          </div>
          <div className="flex-[4] min-w-0">
            <DecisionFeed key={ep.events} decisionsUrl={ep.decisions} eventsUrl={ep.events} />
          </div>
        </div>
      </main>
    </div>
  )
}
