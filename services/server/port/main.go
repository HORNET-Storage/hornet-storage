package main

import (
	"context"
	"flag"
	"os"
	"syscall"

	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/services/server/core"
	"github.com/HORNET-Storage/hornet-storage/services/server/lifecycle"
)

var (
	compactDB      = flag.Bool("compact", false, "Run database compaction to reclaim any potential disk space before starting regular services")
	memoryProfiler = flag.Bool("profile", false, "Run pprof memory profiler enabling memory usage debugging")
	bootstrapSetup = flag.Bool("bootstrap-setup", false, "Run first-time setup server before starting relay services")
	setupHost      = flag.String("setup-host", "127.0.0.1", "Host/interface for first-time setup server")
	setupPort      = flag.Int("setup-port", 11012, "Port for first-time setup server")
	setupProfile   = flag.String("setup-profile", "nosis", "First-time setup profile: nosis or operator")
)

func init() {
	// Parse command-line flags early
	flag.Parse()

	// Initialize config and logging. Run initializes UPnP after any first-time
	// setup configuration has been applied and reloaded.
	core.Initialize()
}

func main() {
	stop := lifecycle.StopChannel(
		os.Stdin,
		lifecycle.WatchParentStdin(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)

	if err := core.Run(context.Background(), core.Options{
		CompactDB:      *compactDB,
		MemoryProfiler: *memoryProfiler,
		BootstrapSetup: *bootstrapSetup,
		SetupHost:      *setupHost,
		SetupPort:      *setupPort,
		SetupProfile:   *setupProfile,
		Stop:           stop,
	}); err != nil {
		logging.Fatalf("Relay exited with error: %v", err)
	}
}
