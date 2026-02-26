package registry

import (
	"net/netip"
	"strings"
	"sync"
)

// PortMapping represents a proxy from the allocated IP to the Docker-assigned host port.
type PortMapping struct {
	ContainerPort uint16 // the original port (e.g. 5432)
	HostPort      uint16 // the Docker-assigned ephemeral host port
}

// Entry represents a tracked container.
type Entry struct {
	ContainerID    string        `json:"container_id"`
	ContainerName  string        `json:"container_name"`
	ComposeProject string        `json:"compose_project,omitempty"`
	ComposeService string        `json:"compose_service,omitempty"`
	IP             netip.Addr    `json:"ip"`
	Ports          []PortMapping `json:"ports"`
}

// DisplayName returns the human-readable DNS name (without trailing dot).
func (e *Entry) DisplayName() string {
	if e.ComposeProject != "" && e.ComposeService != "" {
		return e.ComposeService + "." + e.ComposeProject + ".loop.test"
	}
	name := strings.TrimPrefix(e.ContainerName, "/")
	return name + ".loop.test"
}

// DNSName returns the fully-qualified DNS name (with trailing dot for DNS protocol).
func (e *Entry) DNSName() string {
	return e.DisplayName() + "."
}

// Registry is the central state store mapping containers to IPs and proxies.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]*Entry // keyed by container ID
}

func New() *Registry {
	return &Registry{
		entries: make(map[string]*Entry),
	}
}

// Add registers a container entry.
func (r *Registry) Add(entry *Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[entry.ContainerID] = entry
}

// Remove deletes an entry by container ID and returns it (or nil).
func (r *Registry) Remove(containerID string) *Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[containerID]
	delete(r.entries, containerID)
	return entry
}

// Get returns an entry by container ID.
func (r *Registry) Get(containerID string) *Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.entries[containerID]
}

// GetByDNSName finds an entry matching the given fully-qualified DNS name.
func (r *Registry) GetByDNSName(name string) *Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, entry := range r.entries {
		if entry.DNSName() == name {
			return entry
		}
	}
	return nil
}

// All returns a copy of all entries.
func (r *Registry) All() []*Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Entry, 0, len(r.entries))
	for _, entry := range r.entries {
		result = append(result, entry)
	}
	return result
}
