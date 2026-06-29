package metrics

import "testing"

func BenchmarkMockProvider_GetClusterSnapshot(b *testing.B) {
	p := NewMockProvider("mock-cluster")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = p.GetClusterSnapshot()
	}
}

func BenchmarkMockProvider_Parallel(b *testing.B) {
	p := NewMockProvider("mock-cluster")
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = p.GetClusterSnapshot()
		}
	})
}
