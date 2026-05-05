// Command scrinium-webdav exposes a Scrinium store via WebDAV.
// Cross-platform — Linux/macOS/Windows clients connect over HTTP
// without kernel extensions or root.
//
// Subcommands:
//
//	scrinium-webdav serve --store-path=... --listen=:8080 [flags]
//	scrinium-webdav help
//
// See docs/4 §15 WebDAV Mount for the full specification.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "init":
		os.Exit(runInit(os.Args[2:]))
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

const usageText = `scrinium-webdav — expose a Scrinium store via WebDAV.

Usage:
  scrinium-webdav <command> [flags]

Commands:
  init      Create and initialise a new Scrinium store.
  serve     Start the WebDAV server.
  help      Show this help message.

Run "scrinium-webdav <command> --help" for command-specific flags.

Specification: docs/4 §15 WebDAV Mount.
`

func printUsage(w *os.File) {
	fmt.Fprint(w, usageText)
}
