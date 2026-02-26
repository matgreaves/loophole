package main

import (
	"fmt"
	"os"

	"github.com/matgreaves/loophole/internal/resolver"
)

const usage = `Usage: loophole <command>

Commands:
  serve              Start the loophole daemon
  ls                 List proxied containers
  resolve-install    Install macOS resolver for *.loop.test (requires sudo)
  resolve-uninstall  Remove macOS resolver for *.loop.test (requires sudo)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe()
	case "ls":
		err = runLs()
	case "resolve-install":
		err = resolver.Install()
	case "resolve-uninstall":
		err = resolver.Uninstall()
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
