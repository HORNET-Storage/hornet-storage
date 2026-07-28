//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"

	"github.com/HORNET-Storage/hornet-storage/services/server/core"
)

const (
	// startupWaitHint tells the SCM how long initial startup may take
	// (config, logging, and UPnP discovery happen before RUNNING).
	startupWaitHint = 60 * time.Second

	// stopWaitHint is refreshed with every STOP_PENDING checkpoint while the
	// core drains websockets and closes the database.
	stopWaitHint = 30 * time.Second
)

// relayService adapts the shared relay core to the Windows Service Control
// Manager lifecycle.
type relayService struct {
	elog *eventlog.Log
}

func (s *relayService) logInfo(msg string) {
	if s.elog != nil {
		_ = s.elog.Info(1, msg)
	}
}

func (s *relayService) logError(msg string) {
	if s.elog != nil {
		_ = s.elog.Error(1, msg)
	}
}

// runService executes under the Service Control Manager and blocks until
// the service stops.
func runService() {
	// Best-effort Event Log handle; the installer registers the source.
	// Lifecycle logging must never block service startup.
	elog, err := eventlog.Open(serviceName)
	if err != nil {
		elog = nil
	} else {
		defer elog.Close()
	}

	s := &relayService{elog: elog}

	s.logInfo(fmt.Sprintf("%s service starting", serviceName))
	if err := svc.Run(serviceName, s); err != nil {
		s.logError(fmt.Sprintf("%s service failed: %v", serviceName, err))
		os.Exit(1)
	}
	s.logInfo(fmt.Sprintf("%s service stopped", serviceName))
}

// Execute implements svc.Handler. It reports START_PENDING while the shared
// core initializes, RUNNING once the relay lifecycle is started (the
// first-run bootstrap-setup phase executes inside RUNNING, matching the
// Linux systemd unit), and STOP_PENDING with checkpoints while the graceful
// shutdown drains connections and closes the database.
func (s *relayService) Execute(args []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	status <- svc.Status{State: svc.StartPending, WaitHint: uint32(startupWaitHint / time.Millisecond)}

	// Initialize config and logging. The shared Run lifecycle initializes UPnP
	// only after first-time setup has applied and reloaded the final config.
	core.Initialize()

	stop := make(chan struct{})
	runDone := make(chan error, 1)
	go func() {
		runDone <- core.Run(context.Background(), options(stop))
	}()

	status <- svc.Status{State: svc.Running, Accepts: accepted}
	s.logInfo(fmt.Sprintf("%s entered RUNNING state", serviceName))

	checkpointTicker := time.NewTicker(time.Second)
	defer checkpointTicker.Stop()

	var checkpoint uint32
	var progress <-chan time.Time // nil until a stop is requested
	stopping := false

	for {
		select {
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				status <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				if stopping {
					break
				}
				stopping = true
				s.logInfo(fmt.Sprintf("%s stop requested - beginning graceful shutdown", serviceName))
				checkpoint++
				status <- svc.Status{State: svc.StopPending, CheckPoint: checkpoint, WaitHint: uint32(stopWaitHint / time.Millisecond)}
				close(stop)
				progress = checkpointTicker.C
			}
		case <-progress:
			// Keep the SCM informed while the graceful shutdown runs.
			checkpoint++
			status <- svc.Status{State: svc.StopPending, CheckPoint: checkpoint, WaitHint: uint32(stopWaitHint / time.Millisecond)}
		case err := <-runDone:
			if err != nil {
				s.logError(fmt.Sprintf("%s exited with error: %v", serviceName, err))
				status <- svc.Status{State: svc.StopPending}
				return true, 1
			}
			if stopping {
				s.logInfo(fmt.Sprintf("%s graceful shutdown complete", serviceName))
			} else {
				s.logInfo(fmt.Sprintf("%s run loop ended - stopping service", serviceName))
			}
			status <- svc.Status{State: svc.StopPending}
			return false, 0
		}
	}
}
