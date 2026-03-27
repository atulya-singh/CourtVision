import { useEffect, useState } from 'react'

interface PodMetrics {
  pod_name: string
  namespace: string
  node_name: string
  cpu_usage_milli: number
  cpu_limit_milli: number
  mem_usage_mb: number
  mem_limit_mb: number
  restart_count: number
}

interface NodeMetrics {
  node_name: string
  node_type: string
  cpu_capacity_milli: number
  cpu_used_milli: number
  mem_capacity_mb: number
  mem_used_mb: number
  pod_count: number
}

interface ClusterSnapshot {
  pods: PodMetrics[]
  nodes: NodeMetrics[]
}

function barColor(pct: number): string {
  if (pct >= 90) return 'bg-red-500'
  if (pct >= 70) return 'bg-yellow-500'
  return 'bg-green-500'
}

function PressureBar({ label, pct }: { label: string; pct: number }) {
  const clamped = Math.min(100, Math.max(0, pct))
  return (
    <div className="mt-1">
      <div className="flex justify-between text-xs text-gray-400 mb-0.5">
        <span>{label}</span>
        <span>{clamped.toFixed(1)}%</span>
      </div>
      <div className="h-2 bg-white/10 rounded-full overflow-hidden">
        <div
          className={`h-full rounded-full transition-all duration-500 ${barColor(clamped)}`}
          style={{ width: `${clamped}%` }}
        />
      </div>
    </div>
  )
}

function PodCard({ pod }: { pod: PodMetrics }) {
  const cpuPct = pod.cpu_limit_milli > 0 ? (pod.cpu_usage_milli / pod.cpu_limit_milli) * 100 : 0
  const memPct = pod.mem_limit_mb > 0 ? (pod.mem_usage_mb / pod.mem_limit_mb) * 100 : 0
  const isHot = cpuPct > 90 || memPct > 90

  return (
    <div
      className={`rounded-lg p-3 backdrop-blur-md border transition-all duration-300 ${
        isHot
          ? 'bg-red-500/10 border-red-500/40 shadow-lg shadow-red-500/10'
          : 'bg-white/5 border-white/10'
      }`}
    >
      <div className="flex justify-between items-center mb-2">
        <span className="text-sm font-medium text-white truncate">{pod.pod_name}</span>
        {pod.restart_count > 0 && (
          <span className="text-xs text-red-400 ml-2">R:{pod.restart_count}</span>
        )}
      </div>
      <PressureBar label="CPU" pct={cpuPct} />
      <PressureBar label="MEM" pct={memPct} />
    </div>
  )
}

function NodeCard({ node, pods }: { node: NodeMetrics; pods: PodMetrics[] }) {
  const cpuPressure = node.cpu_capacity_milli > 0
    ? (node.cpu_used_milli / node.cpu_capacity_milli) * 100
    : 0
  const memPressure = node.mem_capacity_mb > 0
    ? (node.mem_used_mb / node.mem_capacity_mb) * 100
    : 0

  return (
    <div className="rounded-xl p-4 backdrop-blur-md bg-white/5 border border-white/10">
      <div className="flex justify-between items-center mb-3">
        <div>
          <h3 className="text-white font-semibold">{node.node_name}</h3>
          <span className="text-xs text-gray-400">{node.node_type}</span>
        </div>
        <span className="text-xs text-gray-500">{node.pod_count} pods</span>
      </div>
      <PressureBar label="CPU Pressure" pct={cpuPressure} />
      <PressureBar label="Memory Pressure" pct={memPressure} />
      <div className="mt-3 grid gap-2">
        {pods.map((pod) => (
          <PodCard key={pod.pod_name} pod={pod} />
        ))}
      </div>
    </div>
  )
}

export default function ClusterOverview() {
  const [snapshot, setSnapshot] = useState<ClusterSnapshot | null>(null)

  useEffect(() => {
    const fetchCluster = () => {
      fetch('/api/cluster')
        .then((r) => r.json())
        .then(setSnapshot)
        .catch(console.error)
    }
    fetchCluster()
    const interval = setInterval(fetchCluster, 3000)
    return () => clearInterval(interval)
  }, [])

  if (!snapshot) {
    return (
      <div className="flex items-center justify-center h-64 text-gray-500">
        Loading cluster data...
      </div>
    )
  }

  const podsByNode: Record<string, PodMetrics[]> = {}
  for (const pod of snapshot.pods) {
    if (!podsByNode[pod.node_name]) podsByNode[pod.node_name] = []
    podsByNode[pod.node_name].push(pod)
  }

  return (
    <div>
      <h2 className="text-lg font-semibold text-white mb-4">Cluster Overview</h2>
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        {snapshot.nodes.map((node) => (
          <NodeCard
            key={node.node_name}
            node={node}
            pods={podsByNode[node.node_name] || []}
          />
        ))}
      </div>
    </div>
  )
}
