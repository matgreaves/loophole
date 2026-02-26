package registry

import (
	"net/netip"
	"testing"
)

func TestDNSNameCompose(t *testing.T) {
	e := &Entry{
		ComposeProject: "myapp",
		ComposeService: "postgres",
	}
	want := "postgres.myapp.loop.test."
	if got := e.DNSName(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDNSNameStandalone(t *testing.T) {
	e := &Entry{
		ContainerName: "/my-container",
	}
	want := "my-container.loop.test."
	if got := e.DNSName(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAddGetRemove(t *testing.T) {
	r := New()
	entry := &Entry{
		ContainerID:   "abc123",
		ContainerName: "/test",
		IP:            netip.MustParseAddr("127.0.0.2"),
	}

	r.Add(entry)

	got := r.Get("abc123")
	if got == nil {
		t.Fatal("expected entry, got nil")
	}
	if got.ContainerName != "/test" {
		t.Fatalf("got name %q", got.ContainerName)
	}

	removed := r.Remove("abc123")
	if removed == nil {
		t.Fatal("expected removed entry")
	}

	if r.Get("abc123") != nil {
		t.Fatal("entry should be removed")
	}
}

func TestGetByDNSName(t *testing.T) {
	r := New()
	r.Add(&Entry{
		ContainerID:    "c1",
		ComposeProject: "myapp",
		ComposeService: "postgres",
		IP:             netip.MustParseAddr("127.0.0.2"),
	})
	r.Add(&Entry{
		ContainerID:    "c2",
		ComposeProject: "myapp",
		ComposeService: "redis",
		IP:             netip.MustParseAddr("127.0.0.3"),
	})

	got := r.GetByDNSName("postgres.myapp.loop.test.")
	if got == nil {
		t.Fatal("expected entry")
	}
	if got.ContainerID != "c1" {
		t.Fatalf("expected c1, got %s", got.ContainerID)
	}

	if r.GetByDNSName("nonexistent.loop.test.") != nil {
		t.Fatal("expected nil for nonexistent name")
	}
}

func TestAll(t *testing.T) {
	r := New()
	r.Add(&Entry{ContainerID: "c1", ContainerName: "/a"})
	r.Add(&Entry{ContainerID: "c2", ContainerName: "/b"})

	all := r.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(all))
	}
}
