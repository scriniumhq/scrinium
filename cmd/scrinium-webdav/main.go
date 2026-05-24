// Command scrinium-webdav exposes a Scrinium store via WebDAV.
// Cross-platform — Linux/macOS/Windows clients connect over HTTP
// without kernel extensions or root.
//
// Since R11 the daemon is a thin loader over composer: it reads a
// declarative config document and serves the surfaces it declares.
//
//	scrinium-webdav serve --config /etc/scrinium/webdav.yaml
//
// The config is a composer document with a webdav surface, e.g.:
//
//	store:
//	  driver: file:///var/lib/scrinium
//	  policy:
//	    encryption:
//	      passphrase: file:/etc/scrinium/passphrase
//	projection:
//	  namespace: files
//	surfaces:
//	  - kind: webdav
//	    config:
//	      listen: ":8080"
//	      allowOsJunk: false
//
// See docs/4 §15 WebDAV Mount for the full specification.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"scrinium.dev/composer"

	// Surface + built-in backends, registered by import side effect.
	_ "scrinium.dev/engine/surface/webdav"
)

func main() {
	if len(os.Args) < 2 {
		printUsage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	case "-h", "--help", "help":
		printUsage(os.Stdout)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "scrinium-webdav: unknown command %q\n\n", os.Args[1])
		printUsage(os.Stderr)
		os.Exit(2)
	}
}

func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "Path to a composer YAML config file (required).")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "scrinium-webdav serve: --config is required")
		return 2
	}

	data, err := os.ReadFile(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav serve: read config: %v\n", err)
		return 2
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	rt, err := composer.LoadOrInitYAML(ctx, data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav: %v\n", err)
		return 1
	}
	defer rt.Close()

	fmt.Fprintf(os.Stderr, "Mount session: %s\n", rt.MountSession())
	if err := rt.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav: %v\n", err)
		return 1
	}
	return 0
}

const usageText = `scrinium-webdav — expose a Scrinium store via WebDAV.

Usage:
  scrinium-webdav serve --config <file>

Commands:
  serve     Load a composer config and serve its surfaces.
  help      Show this help message.

Specification: docs/4 §15 WebDAV Mount.
`

func printUsage(w *os.File) {
	fmt.Fprint(w, usageText)
}
