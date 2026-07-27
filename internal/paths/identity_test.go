package paths

import "testing"

func TestIdentity_DefaultsDevelopmentVersion(t *testing.T) {
	got := (Identity{}).OrDevelopment()
	if got.Product != Product || got.Version != DevelopmentVersion {
		t.Fatalf("identity = %+v, want %s %s", got, Product, DevelopmentVersion)
	}
}

func TestNewIdentity_PreservesReleaseVersion(t *testing.T) {
	got := NewIdentity("v1.2.3")
	if got.Product != Product || got.Version != "v1.2.3" {
		t.Fatalf("identity = %+v, want %s v1.2.3", got, Product)
	}
}
