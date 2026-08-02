// Package logging configures structured logs for the binaries owned by this
// repository. Workload applications remain responsible for their own logs.
package logging

import (
	"log/slog"
	"os"
)

func Configure(component string) {
	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{}).WithAttrs([]slog.Attr{slog.String("component", component)})
	slog.SetDefault(slog.New(handler))
}
