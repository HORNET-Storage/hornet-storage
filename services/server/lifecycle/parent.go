package lifecycle

import (
	"io"
	"os"
	"os/signal"
	"sync"
)

const ParentStdinWatchEnv = "HORNETS_PARENT_STDIN_WATCH"

// StopChannel closes when an OS shutdown signal arrives or, when explicitly
// enabled, when the supervising parent's stdin pipe reaches EOF. Services and
// standalone invocations retain their existing signal-only lifecycle.
func StopChannel(parent io.Reader, watchParent bool, shutdownSignals ...os.Signal) <-chan struct{} {
	stop := make(chan struct{})
	var once sync.Once
	requestStop := func() {
		once.Do(func() { close(stop) })
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, shutdownSignals...)
	go func() {
		select {
		case <-signals:
			requestStop()
		case <-stop:
		}
		signal.Stop(signals)
	}()

	if watchParent {
		go func() {
			_, _ = io.Copy(io.Discard, parent)
			requestStop()
		}()
	}

	return stop
}

// WatchParentStdin reports whether this console process was launched under the
// Nosis session supervisor. It is deliberately opt-in so an ordinary closed
// terminal stdin never stops a service or standalone relay.
func WatchParentStdin() bool {
	return os.Getenv(ParentStdinWatchEnv) == "1"
}
