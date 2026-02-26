package dns

import (
	"net"
	"net/netip"
	"testing"
	"time"

	mdns "github.com/miekg/dns"
)

func findFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.LocalAddr().(*net.UDPAddr).Port
	ln.Close()
	return port
}

func TestDNSServerResolvesARecord(t *testing.T) {
	port := findFreePort(t)
	addr := FormatAddr("127.0.0.1", port)

	resolver := ResolverFunc(func(fqdn string) (netip.Addr, bool) {
		if fqdn == "postgres.myapp.loop.test." {
			return netip.MustParseAddr("127.0.0.2"), true
		}
		return netip.Addr{}, false
	})

	srv := New(addr, resolver)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	// Give server a moment to start
	time.Sleep(50 * time.Millisecond)

	c := new(mdns.Client)
	m := new(mdns.Msg)
	m.SetQuestion("postgres.myapp.loop.test.", mdns.TypeA)

	r, _, err := c.Exchange(m, addr)
	if err != nil {
		t.Fatal(err)
	}

	if len(r.Answer) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(r.Answer))
	}

	a, ok := r.Answer[0].(*mdns.A)
	if !ok {
		t.Fatal("expected A record")
	}
	if a.A.String() != "127.0.0.2" {
		t.Fatalf("expected 127.0.0.2, got %s", a.A)
	}
	if a.Hdr.Ttl != 1 {
		t.Fatalf("expected TTL 1, got %d", a.Hdr.Ttl)
	}
}

func TestDNSServerUnknownName(t *testing.T) {
	port := findFreePort(t)
	addr := FormatAddr("127.0.0.1", port)

	resolver := ResolverFunc(func(fqdn string) (netip.Addr, bool) {
		return netip.Addr{}, false
	})

	srv := New(addr, resolver)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	time.Sleep(50 * time.Millisecond)

	c := new(mdns.Client)
	m := new(mdns.Msg)
	m.SetQuestion("unknown.loop.test.", mdns.TypeA)

	r, _, err := c.Exchange(m, addr)
	if err != nil {
		t.Fatal(err)
	}

	if len(r.Answer) != 0 {
		t.Fatalf("expected 0 answers, got %d", len(r.Answer))
	}
}
