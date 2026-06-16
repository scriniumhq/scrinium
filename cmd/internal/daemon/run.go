package daemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	scrinium "scrinium.dev"
	"scrinium.dev/x/fspath"
)

// Command is a subcommand handler. It receives the args after the
// subcommand name and returns the process exit code.
type Command func(args []string) int

// Dispatch is the shared main() body for the daemon binaries: it
// routes os.Args[1] to a registered Command, handles -h/--help/help
// and the no-args and unknown-command cases by printing usage. name
// is the binary name (for error messages); usage is the full help
// text. Returns the process exit code; callers do os.Exit(Dispatch(…)).
func Dispatch(name, usage string, commands map[string]Command) int {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	switch os.Args[1] {
	case "-h", "--help", "help":
		fmt.Fprint(os.Stdout, usage)
		return 0
	}
	if cmd, ok := commands[os.Args[1]]; ok {
		return cmd(os.Args[2:])
	}
	fmt.Fprintf(os.Stderr, "%s: unknown command %q\n\n", name, os.Args[1])
	fmt.Fprint(os.Stderr, usage)
	return 2
}

// Load reads a Scrinium YAML config and builds the client, wiring a
// signal-cancelled context (SIGINT/SIGTERM) and printing the mount
// session. It is the preamble every daemon's run command shares.
//
// On success it returns the client, the cancellable context, a stop
// func (call via defer: it cancels the context and closes the client),
// and exit code 0. On failure it prints a diagnostic prefixed with
// name and returns a non-zero code; the other returns are nil/no-op
// and the caller should return the code immediately.
//
// requireProjection rejects configs with no projection section — the
// browse/mount/serve daemons all need one. name is the binary name for
// error messages.
func Load(name, configPath string, requireProjection bool) (*scrinium.ScriniumClient, context.Context, func(), int) {
	noop := func() {}
	if configPath == "" {
		fmt.Fprintf(os.Stderr, "%s: --config is required\n", name)
		return nil, nil, noop, 2
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: read config: %v\n", name, err)
		return nil, nil, noop, 2
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	// All three daemons (fuse/webdav/webview) serve the filesystem view,
	// so they all need the by-path extension. Enabled here, in the shared
	// loader, rather than per-daemon (ADR-98 — composition-root opt-in).
	asm, err := scrinium.LoadOrInitYAML(ctx, data, scrinium.WithExtension(fspath.NewExtension()))
	if err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		return nil, nil, noop, 1
	}

	if requireProjection && asm.Projection == nil {
		fmt.Fprintf(os.Stderr, "%s: config has no projection section; nothing to serve\n", name)
		_ = asm.Close()
		cancel()
		return nil, nil, noop, 1
	}

	stop := func() {
		_ = asm.Close()
		cancel()
	}
	fmt.Fprintf(os.Stderr, "Mount session: %s\n", asm.MountSession)
	return asm, ctx, stop, 0
}

// ServeHTTP runs srv until ctx is cancelled, then gracefully shuts it
// down with a 5s timeout. Returns the process exit code: 0 on a clean
// shutdown, 1 on a listen error (prefixed with name). Blocks until the
// server stops.
func ServeHTTP(ctx context.Context, name string, srv *http.Server) int {
	go func() {
		<-ctx.Done()
		shutCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(shutCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		return 1
	}
	return 0
}
