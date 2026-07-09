package metrics

import (
	"testing"

	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
)

// TestProbeVersion_Reachable: a responsive API server yields Reachable=true, its
// reported version, the resolved context, and no error.
func TestProbeVersion_Reachable(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.29.0"}

	st := probeVersion(client, "kind-kind")
	if !st.Reachable {
		t.Fatalf("expected reachable, got err=%v", st.Err)
	}
	if st.ServerVersion != "v1.29.0" {
		t.Errorf("server version = %q, want v1.29.0", st.ServerVersion)
	}
	if st.Context != "kind-kind" {
		t.Errorf("context = %q, want kind-kind", st.Context)
	}
	if st.Err != nil {
		t.Errorf("a reachable status should carry no error, got %v", st.Err)
	}
}

// TestProbeVersion_EmptyVersionStillReachable: even when the server reports no
// version string, a successful discovery call still counts as reachable — the
// probe's job is liveness, not version reporting.
func TestProbeVersion_EmptyVersionStillReachable(t *testing.T) {
	st := probeVersion(fake.NewSimpleClientset(), "ctx")
	if !st.Reachable {
		t.Errorf("a successful discovery call should be reachable, got err=%v", st.Err)
	}
}
