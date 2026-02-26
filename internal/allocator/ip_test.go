package allocator

import (
	"net/netip"
	"testing"
)

func TestAllocateSequential(t *testing.T) {
	a := New()

	ip1, err := a.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if ip1 != netip.MustParseAddr("127.0.0.2") {
		t.Fatalf("expected 127.0.0.2, got %s", ip1)
	}

	ip2, err := a.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if ip2 != netip.MustParseAddr("127.0.0.3") {
		t.Fatalf("expected 127.0.0.3, got %s", ip2)
	}
}

func TestSkipsReservedIPs(t *testing.T) {
	a := New()

	seen := make(map[netip.Addr]bool)
	for i := 0; i < 60; i++ {
		ip, err := a.Allocate()
		if err != nil {
			t.Fatal(err)
		}
		seen[ip] = true
	}

	if seen[netip.MustParseAddr("127.0.0.1")] {
		t.Fatal("should not allocate 127.0.0.1")
	}
}

func TestReleaseAndReuse(t *testing.T) {
	a := New()

	ip1, _ := a.Allocate()
	ip2, _ := a.Allocate()
	_ = ip2

	a.Release(ip1)

	ip3, err := a.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if ip3 != ip1 {
		t.Fatalf("expected reuse of %s, got %s", ip1, ip3)
	}
}

func TestReleaseLIFO(t *testing.T) {
	a := New()

	ip1, _ := a.Allocate()
	ip2, _ := a.Allocate()

	a.Release(ip1)
	a.Release(ip2)

	// LIFO: ip2 should come back first
	ip3, _ := a.Allocate()
	if ip3 != ip2 {
		t.Fatalf("expected LIFO reuse of %s, got %s", ip2, ip3)
	}

	ip4, _ := a.Allocate()
	if ip4 != ip1 {
		t.Fatalf("expected LIFO reuse of %s, got %s", ip1, ip4)
	}
}

func TestDoubleRelease(t *testing.T) {
	a := New()

	ip, _ := a.Allocate()
	a.Release(ip)
	a.Release(ip) // should be safe

	got, _ := a.Allocate()
	if got != ip {
		t.Fatalf("expected %s, got %s", ip, got)
	}

	// Next allocate should NOT return ip again (only freed once effectively)
	got2, _ := a.Allocate()
	if got2 == ip {
		t.Fatal("double release caused duplicate allocation")
	}
}
