import ClusterOverview from './ClusterOverview'
import DecisionFeed from './DecisionFeed'

export default function Dashboard() {
  return (
    <div className="min-h-screen bg-gray-900 text-white p-4">
      <h1 className="text-2xl font-bold mb-4">Dashboard</h1>
      <div className="flex gap-4">
        <div className="flex-[6]">
          <ClusterOverview />
        </div>
        <div className="flex-[4]">
          <DecisionFeed />
        </div>
      </div>
    </div>
  )
}
