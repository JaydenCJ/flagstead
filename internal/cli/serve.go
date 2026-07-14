// serve: bind the HTTP API. The listener is bound before the "serving"
// line is printed, so once that line appears the API is ready — the smoke
// script and any process supervisor can rely on it. Passing an explicit
// port 0 picks a free port and prints the real one.
package cli

import (
	"fmt"
	"io"
	"net"

	"github.com/JaydenCJ/flagstead/internal/server"
	"github.com/JaydenCJ/flagstead/internal/version"
)

func cmdServe(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("serve", stderr)
	file := fs.String("file", "flags.toml", "flag file to serve")
	addr := fs.String("addr", "127.0.0.1:4949", "listen address (loopback by default)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 0 {
		fmt.Fprintln(stderr, "flagstead: serve takes no positional arguments")
		return 2
	}

	// Validate through the same path as `flagstead check`, so startup
	// failures carry the same per-problem messages and exit codes.
	if _, code := loadFile(*file, stderr); code != 0 {
		return code
	}
	store, err := server.NewStore(*file)
	if err != nil {
		fmt.Fprintf(stderr, "flagstead: %v\n", err)
		return 3
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(stderr, "flagstead: %v\n", err)
		return 3
	}
	fmt.Fprintf(stdout, "flagstead %s serving %s on http://%s\n",
		version.Version, *file, ln.Addr())

	if err := server.New(store).HTTPServer().Serve(ln); err != nil {
		fmt.Fprintf(stderr, "flagstead: %v\n", err)
		return 3
	}
	return 0
}
