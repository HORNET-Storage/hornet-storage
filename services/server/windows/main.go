//go:build windows

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/svc"

	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/services/server/core"
)

// serviceName is the Windows service identity shared by the installer
// (manage-windows-service.ps1), the tray application, and the Event Log
// source.
const serviceName = "HornetsRelay"

var (
	compactDB      = flag.Bool("compact", false, "Run database compaction to reclaim any potential disk space before starting regular services")
	memoryProfiler = flag.Bool("profile", false, "Run pprof memory profiler enabling memory usage debugging")
	bootstrapSetup = flag.Bool("bootstrap-setup", false, "Run first-time setup server before starting relay services")
	setupHost      = flag.String("setup-host", "127.0.0.1", "Host/interface for first-time setup server")
	setupPort      = flag.Int("setup-port", 11012, "Port for first-time setup server")
	setupProfile   = flag.String("setup-profile", "nosis", "First-time setup profile: nosis or operator")
)

func main() {
	// Parse command-line flags early. The SCM passes the service ImagePath
	// arguments through os.Args exactly like a console invocation.
	flag.Parse()

	if err := resolveEnvironment(); err != nil {
		// Pre-Initialize failure: neither config nor logging exist yet.
		fmt.Fprintf(os.Stderr, "failed to prepare relay environment: %v\n", err)
		os.Exit(1)
	}

	isService, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to detect Windows service context: %v\n", err)
		os.Exit(1)
	}

	if isService {
		runService()
		return
	}

	runConsole()
}

// options assembles the shared core run options from the parsed flags.
func options(stop <-chan struct{}) core.Options {
	return core.Options{
		CompactDB:      *compactDB,
		MemoryProfiler: *memoryProfiler,
		BootstrapSetup: *bootstrapSetup,
		SetupHost:      *setupHost,
		SetupPort:      *setupPort,
		SetupProfile:   *setupProfile,
		Stop:           stop,
	}
}

// runConsole behaves like the services/server/port build: core.Initialize
// brings up config and logging, Run initializes UPnP from the final post-setup
// configuration, and SIGINT/SIGTERM trigger the same graceful shutdown.
func runConsole() {
	// Initialize config and logging before entering the shared run lifecycle
	core.Initialize()

	// Convert OS kill signals into the core stop channel
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	stop := make(chan struct{})
	go func() {
		<-sigs
		close(stop)
	}()

	if err := core.Run(context.Background(), options(stop)); err != nil {
		logging.Fatalf("Relay exited with error: %v", err)
	}
}

// resolveEnvironment reproduces in-process what the retired
// scripts/windows/start-relay.ps1 wrapper did for the scheduled task:
//
//  1. change into the relay working directory - the relay's config system is
//     working-directory based (config.yaml, data/, and logs live there),
//  2. prepend the executable's own directory to the process PATH so the
//     bundled hyperswarm sidecar tooling resolves,
//  3. default AIRLOCK_CONFIG_PATH to the sibling airlock config so the
//     relay-driven bootstrap keeps writing both configs.
//
// The working directory defaults to %ProgramData%\Hornet Storage\relay and
// can be overridden with the HORNETS_RELAY_DIR environment variable.
func resolveEnvironment() error {
	dir := strings.TrimSpace(os.Getenv("HORNETS_RELAY_DIR"))
	if dir == "" {
		programData := os.Getenv("ProgramData")
		if programData == "" {
			programData = `C:\ProgramData`
		}
		dir = filepath.Join(programData, "Hornet Storage", "relay")
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create working directory %s: %w", dir, err)
	}
	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("enter working directory %s: %w", dir, err)
	}

	if exePath, err := os.Executable(); err == nil {
		prependToPath(filepath.Dir(exePath))
	}

	if strings.TrimSpace(os.Getenv("AIRLOCK_CONFIG_PATH")) == "" {
		airlockConfig := filepath.Join(filepath.Dir(dir), "airlock", "config.yaml")
		if err := os.Setenv("AIRLOCK_CONFIG_PATH", airlockConfig); err != nil {
			return fmt.Errorf("set AIRLOCK_CONFIG_PATH: %w", err)
		}
	}

	return nil
}

// prependToPath puts dir at the front of the process PATH unless it is
// already listed.
func prependToPath(dir string) {
	current := os.Getenv("PATH")
	for _, entry := range strings.Split(current, string(os.PathListSeparator)) {
		if strings.EqualFold(strings.TrimSpace(entry), dir) {
			return
		}
	}
	if current == "" {
		_ = os.Setenv("PATH", dir)
		return
	}
	_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+current)
}
