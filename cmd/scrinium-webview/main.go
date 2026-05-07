// Command scrinium-webview serves a read-only HTML view of a
// Scrinium store over HTTP. It's a diagnostic surface — point a
// browser at it to inspect artifacts, navigate the by-path /
// by-date / by-namespace trees, search by id or path, and read
// projection stats.
//
// scrinium-webview is the read-only sibling of scrinium-webdav.
// They share the same daemon library (internal/daemon) and the
// same HTML implementation (internal/scriniumweb), differing
// only in surface: webdav exposes WebDAV protocol on the same
// port as the HTML browser; webview exposes only the browser
// and forces ReadOnly=true on the daemon.
//
// Run multiple binaries against the same on-disk store to
// observe the typical "one store, several surfaces" deployment:
//
//	scrinium-fuse mount    --store=/path/to/store ...
//	scrinium-webdav serve  --store=/path/to/store --listen=:8080
//	scrinium-webview serve --store=/path/to/store --listen=:8081
//
// SQLite WAL is happy hosting all three on the same machine; for
// multi-host deployments a postgres backend (planned) becomes
// necessary.
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
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	case "-h", "--help", "help":
		printUsage(os.Stdout)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "scrinium-webview: unknown command %q\n\n", os.Args[1])
		printUsage(os.Stderr)
		os.Exit(2)
	}
}

const usageText = `scrinium-webview — read-only HTML view of a Scrinium store.

Usage:
  scrinium-webview <command> [flags]

Commands:
  serve   Open the store and serve the HTML browser over HTTP.
  help    Show this help message.

Run "scrinium-webview serve --help" for command-specific flags.
`

func printUsage(w *os.File) {
	fmt.Fprint(w, usageText)
}
