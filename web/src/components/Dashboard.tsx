import { useEffect, useState } from 'react'
import ClusterOverview from './ClusterOverview'
import DecisionFeed from './DecisionFeed'

function StatsBar() {
  const [stats, setStats] = useState({ total: 0, critical: 0 })
  const [uptime, setUptime] = useState(0)

  useEffect(() => {
    const fetchStats = () => {
      fetch('/api/decisions')
        .then((r) => r.json())
        .then((decisions: { severity: string }[]) => {
          if (Array.isArray(decisions)) {
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
    return () => clearInterval(interval)
  }, [])

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

export default function Dashboard() {
  return (
    <div className="min-h-screen bg-gradient-to-br from-gray-950 via-gray-900 to-gray-950 text-white">
      <header className="border-b border-white/10 backdrop-blur-md bg-white/5">
        <div className="max-w-[1600px] mx-auto px-6 py-4 flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-bold tracking-tight">CourtVision</h1>
            <p className="text-sm text-gray-400">Agentic Infrastructure Monitor</p>
          </div>
          <StatsBar />
        </div>
      </header>
      <main className="max-w-[1600px] mx-auto px-6 py-6">
        <div className="flex gap-6">
          <div className="flex-[6] min-w-0">
            <ClusterOverview />
          </div>
          <div className="flex-[4] min-w-0">
            <DecisionFeed />
          </div>
        </div>
      </main>
    </div>
  )
}
