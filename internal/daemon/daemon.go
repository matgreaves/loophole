package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sync"

	"github.com/matgreaves/loophole/internal/allocator"
	"github.com/matgreaves/loophole/internal/dns"
	"github.com/matgreaves/loophole/internal/docker"
	"github.com/matgreaves/loophole/internal/loopback"
	"github.com/matgreaves/loophole/internal/proxy"
	"github.com/matgreaves/loophole/internal/registry"
)

const maxAllocRetries = 10

// Daemon orchestrates all components.
type Daemon struct {
	allocator *allocator.IPAllocator
	registry  *registry.Registry
	dns       *dns.Server
	watcher   *docker.Watcher

	mu      sync.Mutex
	proxies map[string][]proxy.Proxy // keyed by container ID
}

func New() (*Daemon, error) {
	w, err := docker.NewWatcher()
	if err != nil {
		return nil, err
	}

	reg := registry.New()
	alloc := allocator.New()

	resolver := &dns.RegistryResolver{
		Lookup: func(name string) (netip.Addr, bool) {
			entry := reg.GetByDNSName(name)
			if entry == nil {
				return netip.Addr{}, false
			}
			return entry.IP, true
		},
	}

	dnsServer := dns.New(dns.FormatAddr("127.0.0.1", 15353), resolver)

	return &Daemon{
		allocator: alloc,
		registry:  reg,
		dns:       dnsServer,
		watcher:   w,
		proxies:   make(map[string][]proxy.Proxy),
	}, nil
}

// Run starts the daemon and blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.dns.Start(); err != nil {
		return fmt.Errorf("dns server: %w", err)
	}
	defer d.dns.Stop()

	slog.Info("loophole daemon started")

	events := d.watcher.Watch(ctx)

	for ev := range events {
		switch ev.Type {
		case "start":
			d.handleStart(ctx, ev.Info)
		case "stop":
			d.handleStop(ev.Info.ID)
		}
	}

	// Cleanup on shutdown
	d.mu.Lock()
	for id := range d.proxies {
		d.stopProxies(id)
	}
	d.mu.Unlock()

	for _, entry := range d.registry.All() {
		loopback.RemoveAlias(entry.IP.String())
	}

	return nil
}

func (d *Daemon) handleStart(ctx context.Context, info docker.ContainerInfo) {
	if len(info.Ports) == 0 {
		return
	}

	slog.Info("container started",
		"name", info.Name,
		"id", info.ID[:12],
		"ports", len(info.Ports),
	)

	var (
		ip      netip.Addr
		proxies []proxy.Proxy
		err     error
	)

	for attempt := 0; attempt < maxAllocRetries; attempt++ {
		ip, err = d.allocator.Allocate()
		if err != nil {
			slog.Error("ip allocation failed", "err", err)
			return
		}

		if err = loopback.AddAlias(ip.String()); err != nil {
			slog.Warn("loopback alias failed, trying next IP",
				"ip", ip,
				"err", err,
				"attempt", attempt+1,
			)
			d.allocator.Release(ip)
			continue
		}

		proxies, err = d.startProxies(ctx, ip, info.Ports)
		if err != nil {
			slog.Warn("proxy bind failed, trying next IP",
				"ip", ip,
				"err", err,
				"attempt", attempt+1,
			)
			for _, p := range proxies {
				p.Stop()
			}
			loopback.RemoveAlias(ip.String())
			d.allocator.Release(ip)
			proxies = nil
			continue
		}

		break
	}

	if proxies == nil {
		slog.Error("failed to start proxies after retries",
			"name", info.Name,
			"id", info.ID[:12],
		)
		return
	}

	entry := &registry.Entry{
		ContainerID:    info.ID,
		ContainerName:  info.Name,
		ComposeProject: info.ComposeProject,
		ComposeService: info.ComposeService,
		IP:             ip,
	}
	for _, p := range info.Ports {
		entry.Ports = append(entry.Ports, registry.PortMapping{
			ContainerPort: p.ContainerPort,
			HostPort:      p.HostPort,
		})
	}

	d.registry.Add(entry)

	d.mu.Lock()
	d.proxies[info.ID] = proxies
	d.mu.Unlock()

	d.saveState()

	slog.Info("container proxied",
		"name", info.Name,
		"dns", entry.DisplayName(),
		"ip", ip,
	)
}

func (d *Daemon) handleStop(containerID string) {
	entry := d.registry.Remove(containerID)
	if entry == nil {
		return
	}

	slog.Info("container stopped",
		"name", entry.ContainerName,
		"id", containerID[:12],
	)

	d.mu.Lock()
	d.stopProxies(containerID)
	d.mu.Unlock()

	loopback.RemoveAlias(entry.IP.String())
	d.allocator.Release(entry.IP)
	d.saveState()
}

func (d *Daemon) startProxies(ctx context.Context, ip netip.Addr, ports []docker.PortBinding) ([]proxy.Proxy, error) {
	var started []proxy.Proxy

	for _, p := range ports {
		tcp := proxy.NewTCP(ip.String(), p.ContainerPort, p.HostPort)
		if err := tcp.Start(ctx); err != nil {
			// Clean up any that already started
			for _, s := range started {
				s.Stop()
			}
			return started, err
		}
		started = append(started, tcp)
	}

	return started, nil
}

func (d *Daemon) stopProxies(containerID string) {
	proxies := d.proxies[containerID]
	for _, p := range proxies {
		p.Stop()
	}
	delete(d.proxies, containerID)
}

// StateEntry is the JSON-serializable form of a registry entry for state persistence.
type StateEntry struct {
	ContainerID    string `json:"container_id"`
	ContainerName  string `json:"container_name"`
	ComposeProject string `json:"compose_project,omitempty"`
	ComposeService string `json:"compose_service,omitempty"`
	DNSName        string `json:"dns_name"`
	IP             string `json:"ip"`
	Ports          []struct {
		Container uint16 `json:"container"`
		Host      uint16 `json:"host"`
	} `json:"ports"`
}

func (d *Daemon) saveState() {
	entries := d.registry.All()

	state := make([]StateEntry, 0, len(entries))
	for _, e := range entries {
		se := StateEntry{
			ContainerID:    e.ContainerID,
			ContainerName:  e.ContainerName,
			ComposeProject: e.ComposeProject,
			ComposeService: e.ComposeService,
			DNSName:        e.DisplayName(),
			IP:             e.IP.String(),
		}
		for _, p := range e.Ports {
			se.Ports = append(se.Ports, struct {
				Container uint16 `json:"container"`
				Host      uint16 `json:"host"`
			}{Container: p.ContainerPort, Host: p.HostPort})
		}
		state = append(state, se)
	}

	dir := stateDir()
	os.MkdirAll(dir, 0o755)

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		slog.Error("marshal state", "err", err)
		return
	}

	path := filepath.Join(dir, "state.json")
	tmp := path + ".tmp"

	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		slog.Error("write state", "err", err)
		return
	}

	if err := os.Rename(tmp, path); err != nil {
		slog.Error("rename state", "err", err)
	}
}

func stateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".loophole")
}

// StatePath returns the path to the state file.
func StatePath() string {
	return filepath.Join(stateDir(), "state.json")
}
