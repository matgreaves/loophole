package loopback

import (
	"fmt"
	"os/exec"
)

// AddAlias adds a loopback alias (e.g. 127.0.0.2) on macOS.
// Requires root privileges.
func AddAlias(ip string) error {
	out, err := exec.Command("ifconfig", "lo0", "alias", ip).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ifconfig lo0 alias %s: %w: %s", ip, err, out)
	}
	return nil
}

// RemoveAlias removes a loopback alias on macOS.
func RemoveAlias(ip string) error {
	out, err := exec.Command("ifconfig", "lo0", "-alias", ip).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ifconfig lo0 -alias %s: %w: %s", ip, err, out)
	}
	return nil
}
