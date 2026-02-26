package resolver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	resolverDir  = "/etc/resolver"
	resolverFile = "loop.test"
	content      = "nameserver 127.0.0.1\nport 15353\n"
)

// Install creates /etc/resolver/loop.test pointing to our DNS server.
// Requires root privileges.
func Install() error {
	if err := os.MkdirAll(resolverDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w (are you running with sudo?)", resolverDir, err)
	}

	path := filepath.Join(resolverDir, resolverFile)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w (are you running with sudo?)", path, err)
	}

	flushDNSCache()

	fmt.Printf("Created %s\n", path)
	fmt.Println("macOS will now resolve *.loop.test via 127.0.0.1:15353")
	return nil
}

// Uninstall removes /etc/resolver/loop.test.
// Requires root privileges.
func Uninstall() error {
	path := filepath.Join(resolverDir, resolverFile)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("%s does not exist, nothing to remove\n", path)
			return nil
		}
		return fmt.Errorf("remove %s: %w (are you running with sudo?)", path, err)
	}

	flushDNSCache()

	fmt.Printf("Removed %s\n", path)
	return nil
}

func flushDNSCache() {
	if err := exec.Command("dscacheutil", "-flushcache").Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to flush DNS cache: %v\n", err)
	}
	// mDNSResponder also needs a HUP to pick up resolver file changes
	_ = exec.Command("killall", "-HUP", "mDNSResponder").Run()
}

// IsInstalled checks if the resolver file exists.
func IsInstalled() bool {
	_, err := os.Stat(filepath.Join(resolverDir, resolverFile))
	return err == nil
}
