package allocator

import (
	"fmt"
	"net/netip"
	"sync"
)

// IPAllocator assigns unique 127.0.0.x loopback IPs to containers.
// It starts at 127.0.0.2 and skips reserved addresses.
type IPAllocator struct {
	mu       sync.Mutex
	next     uint32 // next octet to try (offset from 127.0.0.0)
	freed    []netip.Addr
	inUse    map[netip.Addr]bool
	reserved map[netip.Addr]bool
}

func New() *IPAllocator {
	reserved := map[netip.Addr]bool{
		netip.MustParseAddr("127.0.0.1"): true, // host loopback
	}
	return &IPAllocator{
		next:     2, // start at 127.0.0.2
		inUse:    make(map[netip.Addr]bool),
		reserved: reserved,
	}
}

// Allocate returns the next available loopback IP.
func (a *IPAllocator) Allocate() (netip.Addr, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Try freed IPs first (LIFO)
	for len(a.freed) > 0 {
		ip := a.freed[len(a.freed)-1]
		a.freed = a.freed[:len(a.freed)-1]
		if !a.inUse[ip] {
			a.inUse[ip] = true
			return ip, nil
		}
	}

	// Allocate sequentially
	for a.next < (1 << 24) { // 127.0.0.0/8 has 2^24 addresses
		ip := netip.AddrFrom4([4]byte{127, byte(a.next >> 16), byte(a.next >> 8), byte(a.next)})
		a.next++

		if a.reserved[ip] || a.inUse[ip] {
			continue
		}

		a.inUse[ip] = true
		return ip, nil
	}

	return netip.Addr{}, fmt.Errorf("no available loopback IPs")
}

// Release returns an IP to the pool for reuse.
func (a *IPAllocator) Release(ip netip.Addr) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.inUse[ip] {
		delete(a.inUse, ip)
		a.freed = append(a.freed, ip)
	}
}
