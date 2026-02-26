package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/matgreaves/loophole/internal/daemon"
)

func runLs() error {
	data, err := os.ReadFile(daemon.StatePath())
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No containers proxied. Is 'loophole serve' running?")
			return nil
		}
		return err
	}

	var entries []daemon.StateEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("parse state: %w", err)
	}

	if len(entries) == 0 {
		fmt.Println("No containers proxied.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DNS NAME\tIP\tPORTS\tCONTAINER")

	for _, e := range entries {
		var ports []string
		for _, p := range e.Ports {
			ports = append(ports, fmt.Sprintf("%d→%d", p.Container, p.Host))
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			e.DNSName,
			e.IP,
			strings.Join(ports, ","),
			e.ContainerName,
		)
	}

	return w.Flush()
}
