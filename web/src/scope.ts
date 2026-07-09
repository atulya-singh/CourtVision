// Scope describes which view the dashboard is showing.
//
//   single  — the server is running single-cluster (monitor); there is one
//             pipeline and the classic /api/* routes back it.
//   fleet   — multi-cluster mode, looking at the coordinator's cross-cluster
//             (advisory) decisions, which the fleet /api/* routes serve.
//   cluster — multi-cluster mode, drilled into one worker's own store via the
//             /api/clusters/{name}/... routes.
export type Scope =
  | { kind: 'single' }
  | { kind: 'fleet' }
  | { kind: 'cluster'; name: string }

// ClusterSummary mirrors the backend clusterSummary returned by GET /api/clusters.
export interface ClusterSummary {
  name: string
  pod_count: number
  node_count: number
  decision_count: number
}

export interface Endpoints {
  snapshot: string
  decisions: string
  events: string
}

// endpointsFor maps a scope to the three data URLs its views consume. Cluster
// scope hits the per-worker routes; single and fleet both use the fleet-level
// /api/* routes (which back the one store / the coordinator store respectively).
export function endpointsFor(scope: Scope): Endpoints {
  if (scope.kind === 'cluster') {
    const base = `/api/clusters/${encodeURIComponent(scope.name)}`
    return {
      snapshot: `${base}/snapshot`,
      decisions: `${base}/decisions`,
      events: `${base}/events`,
    }
  }
  return {
    snapshot: '/api/cluster',
    decisions: '/api/decisions',
    events: '/api/events',
  }
}
