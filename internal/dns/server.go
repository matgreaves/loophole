package dns

import (
	"fmt"
	"log/slog"
	"net/netip"
	"strings"

	"github.com/miekg/dns"
)

const Domain = "loop.test."

// Resolver looks up an IP for a fully-qualified DNS name.
type Resolver interface {
	ResolveIP(fqdn string) (netip.Addr, bool)
}

// Server is a DNS server that resolves *.loop.test queries.
type Server struct {
	resolver Resolver
	udp      *dns.Server
	tcp      *dns.Server
}

func New(addr string, resolver Resolver) *Server {
	s := &Server{resolver: resolver}

	mux := dns.NewServeMux()
	mux.HandleFunc(Domain, s.handleQuery)

	s.udp = &dns.Server{Addr: addr, Net: "udp", Handler: mux}
	s.tcp = &dns.Server{Addr: addr, Net: "tcp", Handler: mux}

	return s
}

// Start begins serving DNS on both UDP and TCP.
func (s *Server) Start() error {
	udpReady := make(chan struct{})
	tcpReady := make(chan struct{})
	s.udp.NotifyStartedFunc = func() { close(udpReady) }
	s.tcp.NotifyStartedFunc = func() { close(tcpReady) }

	errCh := make(chan error, 2)
	go func() { errCh <- s.udp.ListenAndServe() }()
	go func() { errCh <- s.tcp.ListenAndServe() }()

	// Wait for both to be ready, or return the first error.
	for i := 0; i < 2; i++ {
		select {
		case err := <-errCh:
			return fmt.Errorf("dns listen: %w", err)
		case <-udpReady:
			udpReady = nil
		case <-tcpReady:
			tcpReady = nil
		}
	}

	slog.Info("dns server started", "addr", s.udp.Addr)
	return nil
}

// Stop shuts down the DNS server.
func (s *Server) Stop() {
	if s.udp != nil {
		s.udp.Shutdown()
	}
	if s.tcp != nil {
		s.tcp.Shutdown()
	}
}

func (s *Server) handleQuery(w dns.ResponseWriter, r *dns.Msg) {
	msg := new(dns.Msg)
	msg.SetReply(r)
	msg.Authoritative = true

	for _, q := range r.Question {
		if q.Qtype != dns.TypeA {
			continue
		}

		name := strings.ToLower(q.Name)
		if !strings.HasSuffix(name, Domain) {
			continue
		}

		ip, ok := s.resolver.ResolveIP(name)
		if !ok {
			slog.Debug("dns query not found", "name", name)
			continue
		}

		rr := &dns.A{
			Hdr: dns.RR_Header{
				Name:   q.Name,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    1,
			},
			A: ip.AsSlice(),
		}
		msg.Answer = append(msg.Answer, rr)
		slog.Debug("dns query resolved", "name", name, "ip", ip)
	}

	if err := w.WriteMsg(msg); err != nil {
		slog.Error("dns write error", "err", err)
	}
}

// ResolverFunc adapts a function into the Resolver interface.
type ResolverFunc func(fqdn string) (netip.Addr, bool)

func (f ResolverFunc) ResolveIP(fqdn string) (netip.Addr, bool) {
	return f(fqdn)
}

// RegistryResolver wraps a registry to implement the Resolver interface.
type RegistryResolver struct {
	Lookup func(name string) (netip.Addr, bool)
}

func (r *RegistryResolver) ResolveIP(fqdn string) (netip.Addr, bool) {
	return r.Lookup(fqdn)
}

// FormatAddr formats an address string for the DNS server listen address.
func FormatAddr(ip string, port int) string {
	return fmt.Sprintf("%s:%d", ip, port)
}
