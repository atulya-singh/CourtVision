package store

import (
	"fmt"
	"testing"

	"github.com/atulya-singh/CourtVision/internal/types"
)

func BenchmarkRingBuffer_Write(b *testing.B) {
	rb := NewRingBuffer(1000)
	d := decision("bench")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rb.Write(d)
	}
}

func BenchmarkRingBuffer_ReadAll_Full(b *testing.B) {
	rb := NewRingBuffer(1000)
	for i := 0; i < 1000; i++ {
		rb.Write(decision(fmt.Sprintf("id-%d", i)))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = rb.ReadAll()
	}
}

func BenchmarkStore_AddDecision(b *testing.B) {
	s := New()
	d := types.Decision{ID: "bench", TargetPod: "pod", Namespace: "default"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.AddDecision(d)
	}
}

func BenchmarkStore_GetDecisions(b *testing.B) {
	s := New()
	for i := 0; i < 500; i++ {
		s.AddDecision(types.Decision{ID: fmt.Sprintf("id-%d", i)})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.GetDecisions()
	}
}
