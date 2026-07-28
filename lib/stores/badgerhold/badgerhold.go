package badgerhold

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/dgraph-io/badger/v4"
	"github.com/fxamacker/cbor/v2"
	"github.com/gabriel-vasile/mimetype"
	"github.com/nbd-wtf/go-nostr"
	"go.uber.org/multierr"

	types "github.com/HORNET-Storage/hornet-storage/lib"
	"github.com/HORNET-Storage/hornet-storage/lib/logging"
	"github.com/HORNET-Storage/hornet-storage/lib/stores/statistics"
	statistics_gorm_sqlite "github.com/HORNET-Storage/hornet-storage/lib/stores/statistics/gorm/sqlite"
	"github.com/timshannon/badgerhold/v4"
)

const (
	AddressStatusAvailable = "available"
	AddressStatusAllocated = "allocated"
	AddressStatusUsed      = "used"
)

type BadgerholdStore struct {
	Ctx    context.Context
	cancel context.CancelFunc // Signals GC goroutine and monitor to stop on shutdown

	DatabasePath string
	Database     *badgerhold.Store

	StatsDatabase statistics.StatisticsStore

	searchIndex *bleveSearchIndex
	searchKinds map[int]struct{}

	gcSignal chan struct{} // Non-blocking signal from write paths to trigger extra GC cycle

	closed bool
	mu     sync.RWMutex
}

func cborEncode(value interface{}) ([]byte, error) {
	return cbor.Marshal(value)
}

func cborDecode(data []byte, value interface{}) error {
	return cbor.Unmarshal(data, value)
}

func InitStore(basepath string, args ...interface{}) (*BadgerholdStore, error) {
	// We no longer need to set the top-level moderation_mode as we're using the one in relay_settings

	store := &BadgerholdStore{}

	var err error

	ctx, cancel := context.WithCancel(context.Background())
	store.Ctx = ctx
	store.cancel = cancel
	store.gcSignal = make(chan struct{}, 1)

	store.DatabasePath = basepath

	options := badgerhold.DefaultOptions
	options.Encoder = cborEncode
	options.Decoder = cborDecode
	options.Dir = store.DatabasePath
	options.ValueDir = store.DatabasePath

	// Memory and disk optimization settings for BadgerDB.
	// These prevent excessive RAM and disk usage during heavy operations.
	//
	// Memory estimates for concurrent operations:
	// - Block cache (256 MB): Shared across all operations, speeds up reads
	// - Index cache (128 MB): Keeps block indices in memory for fast lookups
	// - MemTables (3 x 64 MB = 192 MB): Write buffers before flushing to disk
	// - Total baseline: ~576 MB, scales well with 5-10 concurrent uploads/downloads
	//
	// Disk space optimization:
	// - ValueLogFileSize: 256MB is a good balance (not too many files, not too large)
	// - NumVersionsToKeep: Only keep latest version (we don't need history)
	// - ValueThreshold: 32KB threshold - small metadata stays in LSM, large content goes to vlog
	// - CompactL0OnClose: Compact on close to reduce startup time
	//
	// GC Strategy (Adaptive):
	// BadgerDB requires periodic GC via RunValueLogGC() to reclaim dead vlog space.
	// We use an adaptive system that monitors vlog growth rate and adjusts aggressiveness:
	// - Normal:   every 5 min, 0.5 ratio, 20 iterations  (stable/idle)
	// - Elevated: every 1 min, 0.3 ratio, 50 iterations  (moderate writes)
	// - Critical: every 30s,  0.1 ratio, 100 iterations  (heavy writes)
	// Additionally, aggressive GC runs on startup and before shutdown.
	options.Options = options.Options.
		WithBlockCacheSize(256 << 20).   // 256 MB block cache (good for reads)
		WithIndexCacheSize(128 << 20).   // 128 MB index cache (caps memory)
		WithMemTableSize(64 << 20).      // 64 MB memtable size
		WithNumMemtables(3).             // 3 memtables
		WithValueLogFileSize(256 << 20). // 256 MB vlog files (balance between GC granularity and file count)
		WithNumVersionsToKeep(1).        // Only keep 1 version (saves disk space)
		WithCompactL0OnClose(true).      // Compact level 0 on close
		WithValueThreshold(32 << 10)     // 32 KB threshold (small metadata in LSM, content in vlog)

	store.Database, err = badgerhold.Open(options)
	if err != nil {
		logging.Fatalf("Failed to open main database: %v", err)
	}

	// Verify (or stamp) the database schema version.
	// This prevents the relay from running against an un-migrated v1 database.
	if err := CheckSchemaVersion(store.Database.Badger()); err != nil {
		logging.Fatalf("Schema version check failed: %v", err)
	}

	// Check if a custom statistics database path was provided
	var statsDbPath string
	if len(args) > 0 {
		if path, ok := args[0].(string); ok {
			statsDbPath = path
		}
	}

	// Initialize statistics database with optional custom path
	if statsDbPath != "" {
		store.StatsDatabase, err = statistics_gorm_sqlite.InitStore(statsDbPath)
	} else {
		store.StatsDatabase, err = statistics_gorm_sqlite.InitStore()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to initialize gorm statistics database: %v", err)
	}

	// Bleve is a derived NIP-50 index. Failure to initialize it must not make
	// canonical event storage unavailable; readiness controls NIP-50 advertisement.
	if err := store.initializeSearchIndex(); err != nil {
		logging.Infof("NIP-50 search initialization failed; search will remain unavailable: %v", err)
	}

	// Run aggressive GC on startup to clean garbage from previous run.
	// This prevents the delay before the first periodic GC cycle that was causing
	// 100GB+ garbage accumulation after restarts.
	runStartupGC(store.Database.Badger())

	// Start adaptive background GC that monitors vlog growth and adjusts aggressiveness
	go runAdaptiveGC(store)

	// Start disk usage monitoring (logs every 5 minutes)
	go runDiskUsageMonitor(store, 5*time.Minute)

	return store, nil
}

func (store *BadgerholdStore) Cleanup() error {
	store.mu.Lock()
	defer store.mu.Unlock()

	if store.closed {
		return nil
	}
	store.closed = true

	// Signal adaptive GC goroutine and disk monitor to stop
	store.cancel()

	// Brief grace period for goroutines to exit cleanly
	time.Sleep(200 * time.Millisecond)

	// Run final GC pass before closing to minimize garbage left behind for next startup
	runFinalGC(store.Database.Badger())

	var result error

	// Close the derived index before its canonical Badger source.
	result = multierr.Append(result, store.closeSearchIndex())
	result = multierr.Append(result, store.Database.Close())
	result = multierr.Append(result, store.StatsDatabase.Close())

	return result
}

// IsClosed returns true if the store has been closed
func (store *BadgerholdStore) IsClosed() bool {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.closed
}

// RunGC runs garbage collection on demand. This should be called during bulk
// write operations to prevent disk space from growing unbounded.
// It runs with aggressive settings (lower discard ratio) to reclaim space quickly.
// Returns the number of vlog files compacted.
func (store *BadgerholdStore) RunGC() int {
	if store.IsClosed() {
		return 0
	}

	db := store.Database.Badger()
	gcCount := 0

	// Use aggressive discard ratio of 0.3 (compact files with >30% garbage)
	// This is more aggressive than the background GC to handle bulk writes
	for {
		err := db.RunValueLogGC(0.3)
		if err == badger.ErrNoRewrite {
			break
		}
		if err != nil {
			logging.Infof("GC error during bulk operation: %v", err)
			break
		}
		gcCount++
		// Allow more iterations during bulk operations
		if gcCount >= 20 {
			logging.Infof("GC: Stopped after %d iterations during bulk operation", gcCount)
			break
		}
	}

	return gcCount
}

// SignalGC sends a non-blocking signal to the adaptive GC goroutine to run
// an extra GC cycle. Call this after bulk write operations (e.g., StoreLeavesBatch).
// Safe to call from any goroutine — the signal is coalesced if GC is already pending.
func (store *BadgerholdStore) SignalGC() {
	select {
	case store.gcSignal <- struct{}{}:
	default:
		// Signal already pending — GC will run soon
	}
}

// GC pressure levels for adaptive garbage collection
const (
	gcPressureNormal   = 0 // Stable or slow vlog growth: 5 min interval, 0.5 ratio
	gcPressureElevated = 1 // Moderate vlog growth (>256MB): 1 min interval, 0.3 ratio
	gcPressureCritical = 2 // Rapid vlog growth (>1GB): 30s interval, 0.1 ratio
)

// Compact performs a full database compaction using Badger's Flatten operation.
// This should be called when the database has significant bloat from duplicate writes.
// WARNING: This is an expensive operation that may take a long time for large databases.
// Ideally no writes should be happening during this operation.
// The workers parameter controls parallelism (recommended: number of CPU cores).
func (store *BadgerholdStore) Compact(workers int) error {
	if store.IsClosed() {
		return fmt.Errorf("database is closed")
	}

	db := store.Database.Badger()

	// Get current database size for logging
	lsmBefore, vlogBefore := db.Size()
	logging.Infof("Starting database compaction - LSM: %d MB, VLog: %d MB", lsmBefore/(1024*1024), vlogBefore/(1024*1024))

	// Flatten compacts all levels and rewrites all data, deduplicating
	// any keys that have multiple versions
	if workers <= 0 {
		workers = 4 // Default to 4 workers
	}

	err := db.Flatten(workers)
	if err != nil {
		return fmt.Errorf("flatten failed: %w", err)
	}

	// Run value log GC aggressively after flatten to reclaim space
	gcCount := 0
	for {
		err := db.RunValueLogGC(0.1) // Very aggressive: compact files with >10% garbage
		if err == badger.ErrNoRewrite {
			break
		}
		if err != nil {
			logging.Infof("GC after flatten error: %v", err)
			break
		}
		gcCount++
		if gcCount >= 100 { // Allow more iterations for cleanup
			break
		}
	}

	lsmAfter, vlogAfter := db.Size()
	logging.Infof("Compaction complete - LSM: %d MB, VLog: %d MB (GC'd %d vlog files)", lsmAfter/(1024*1024), vlogAfter/(1024*1024), gcCount)
	logging.Infof("Space reclaimed: %d MB", (vlogBefore-vlogAfter)/(1024*1024))

	return nil
}

// GetDatabaseSize returns the current LSM and VLog sizes in bytes
func (store *BadgerholdStore) GetDatabaseSize() (lsm int64, vlog int64) {
	if store.IsClosed() {
		return 0, 0
	}
	return store.Database.Badger().Size()
}

// DiskUsageStats holds disk usage statistics for monitoring
type DiskUsageStats struct {
	LSMSizeMB      int64
	VLogSizeMB     int64
	TotalSizeMB    int64
	VLogFileCount  int
	SSTFileCount   int
	TotalFileCount int
	EventsStored   int64
	EventsDeleted  int64
	LeavesStored   int64
	GCRunCount     int64
	GCReclaimedMB  int64
}

// Global counters for tracking write operations
var (
	eventsStoredCount  atomic.Int64
	eventsDeletedCount atomic.Int64
	// leavesStoredCount is defined in badgerhold_dags.go as storedLeafCount
	gcRunCount       atomic.Int64
	gcReclaimedBytes atomic.Int64
)

// GetDiskUsageStats returns comprehensive disk usage statistics
func (store *BadgerholdStore) GetDiskUsageStats() DiskUsageStats {
	if store.IsClosed() {
		return DiskUsageStats{}
	}

	lsm, vlog := store.Database.Badger().Size()

	// Count files in database directory
	vlogCount := 0
	sstCount := 0
	totalCount := 0

	entries, err := os.ReadDir(store.DatabasePath)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			totalCount++
			name := entry.Name()
			if strings.HasSuffix(name, ".vlog") {
				vlogCount++
			} else if strings.HasSuffix(name, ".sst") {
				sstCount++
			}
		}
	}

	return DiskUsageStats{
		LSMSizeMB:      lsm / (1024 * 1024),
		VLogSizeMB:     vlog / (1024 * 1024),
		TotalSizeMB:    (lsm + vlog) / (1024 * 1024),
		VLogFileCount:  vlogCount,
		SSTFileCount:   sstCount,
		TotalFileCount: totalCount,
		EventsStored:   eventsStoredCount.Load(),
		EventsDeleted:  eventsDeletedCount.Load(),
		LeavesStored:   storedLeafCount.Load(), // from badgerhold_dags.go
		GCRunCount:     gcRunCount.Load(),
		GCReclaimedMB:  gcReclaimedBytes.Load() / (1024 * 1024),
	}
}

// runDiskUsageMonitor logs disk usage statistics periodically
func runDiskUsageMonitor(store *BadgerholdStore, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastStats DiskUsageStats

	for {
		select {
		case <-ticker.C:
			if store.IsClosed() {
				return
			}

			stats := store.GetDiskUsageStats()

			// Calculate deltas
			deltaEvents := stats.EventsStored - lastStats.EventsStored
			deltaDeletes := stats.EventsDeleted - lastStats.EventsDeleted
			deltaLeaves := stats.LeavesStored - lastStats.LeavesStored
			deltaSizeMB := stats.TotalSizeMB - lastStats.TotalSizeMB

			logging.Infof("[DISK MONITOR] Total: %d MB (LSM: %d MB, VLog: %d MB) | Files: %d (.vlog: %d, .sst: %d) | Delta: %+d MB",
				stats.TotalSizeMB, stats.LSMSizeMB, stats.VLogSizeMB,
				stats.TotalFileCount, stats.VLogFileCount, stats.SSTFileCount,
				deltaSizeMB)

			logging.Infof("[DISK MONITOR] Operations since last check: Events stored: %d, deleted: %d | Leaves stored: %d | GC runs: %d (reclaimed: %d MB total)",
				deltaEvents, deltaDeletes, deltaLeaves,
				stats.GCRunCount, stats.GCReclaimedMB)

			lastStats = stats

		case <-store.Ctx.Done():
			return
		}
	}
}

// runAdaptiveGC monitors vlog growth rate and adjusts GC aggressiveness accordingly.
// Three pressure levels: normal (idle), elevated (moderate writes), critical (heavy writes).
// Also listens for explicit signals from bulk write operations via store.gcSignal.
func runAdaptiveGC(store *BadgerholdStore) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	var lastVlogSize int64
	pressure := gcPressureNormal

	for {
		select {
		case <-ticker.C:
			if store.IsClosed() {
				return
			}

			db := store.Database.Badger()

			// Measure vlog growth since last cycle to determine pressure
			_, currentVlog := db.Size()

			if lastVlogSize > 0 {
				growth := currentVlog - lastVlogSize
				switch {
				case growth > 1<<30: // >1 GB growth since last check
					if pressure != gcPressureCritical {
						logging.Info("[GC] Pressure: CRITICAL \u2014 vlog growing rapidly")
					}
					pressure = gcPressureCritical
				case growth > 256<<20: // >256 MB growth
					if pressure != gcPressureElevated {
						logging.Info("[GC] Pressure: ELEVATED \u2014 moderate vlog growth")
					}
					pressure = gcPressureElevated
				default:
					if pressure != gcPressureNormal {
						logging.Info("[GC] Pressure: NORMAL \u2014 vlog growth stable")
					}
					pressure = gcPressureNormal
				}
			}
			lastVlogSize = currentVlog

			runGCCycle(db, pressure)

			// Adjust ticker interval based on new pressure level
			switch pressure {
			case gcPressureCritical:
				ticker.Reset(30 * time.Second)
			case gcPressureElevated:
				ticker.Reset(1 * time.Minute)
			default:
				ticker.Reset(5 * time.Minute)
			}

		case <-store.gcSignal:
			// Bulk write path signaled \u2014 run an elevated GC cycle immediately
			if !store.IsClosed() {
				runGCCycle(store.Database.Badger(), gcPressureElevated)
			}

		case <-store.Ctx.Done():
			return
		}
	}
}

// runGCCycle executes one GC cycle with parameters determined by pressure level.
// Does not block reads/writes \u2014 BadgerDB GC is concurrent-safe.
func runGCCycle(db *badger.DB, pressure int) {
	var discardRatio float64
	var maxIterations int

	switch pressure {
	case gcPressureCritical:
		discardRatio = 0.1 // Very aggressive: compact files with >10% garbage
		maxIterations = 100
	case gcPressureElevated:
		discardRatio = 0.3 // Moderate: compact files with >30% garbage
		maxIterations = 50
	default:
		discardRatio = 0.5 // Conservative: compact files with >50% garbage
		maxIterations = 20
	}

	_, vlogBefore := db.Size()
	gcIterations := 0

	for i := 0; i < maxIterations; i++ {
		if err := db.RunValueLogGC(discardRatio); err != nil {
			break
		}
		gcIterations++
	}

	if gcIterations > 0 {
		gcRunCount.Add(1)
		_, vlogAfter := db.Size()
		if vlogBefore > vlogAfter {
			gcReclaimedBytes.Add(vlogBefore - vlogAfter)
		}

		pressureNames := []string{"normal", "elevated", "critical"}
		logging.Infof("[GC] %s: %d iterations, reclaimed %d MB",
			pressureNames[pressure], gcIterations, (vlogBefore-vlogAfter)/(1024*1024))
	}
}

// runStartupGC runs aggressive garbage collection immediately on startup.
// This cleans up dead vlog space accumulated from the previous run without
// waiting for the first periodic GC cycle (which could be minutes away).
func runStartupGC(db *badger.DB) {
	logging.Info("[GC] Running startup garbage collection...")
	_, vlogBefore := db.Size()
	gcCount := 0

	for i := 0; i < 100; i++ {
		if err := db.RunValueLogGC(0.3); err != nil {
			break
		}
		gcCount++
	}

	if gcCount > 0 {
		_, vlogAfter := db.Size()
		reclaimed := int64(0)
		if vlogBefore > vlogAfter {
			reclaimed = (vlogBefore - vlogAfter) / (1024 * 1024)
		}
		logging.Infof("[GC] Startup GC completed: %d iterations, reclaimed %d MB", gcCount, reclaimed)
	} else {
		logging.Info("[GC] Startup GC: no garbage to collect")
	}
}

// runFinalGC runs garbage collection before database close to minimize
// dead space left behind for the next startup.
func runFinalGC(db *badger.DB) {
	logging.Info("[GC] Running final garbage collection before shutdown...")
	gcCount := 0

	for i := 0; i < 50; i++ {
		if err := db.RunValueLogGC(0.3); err != nil {
			break
		}
		gcCount++
	}

	if gcCount > 0 {
		logging.Infof("[GC] Final GC completed: %d iterations", gcCount)
	}
}

func (store *BadgerholdStore) GetStatsStore() statistics.StatisticsStore {
	return store.StatsDatabase
}

// ExtractMediaURLsFromEvent extracts all media (image and video) URLs from a Nostr event
func ExtractMediaURLsFromEvent(event *nostr.Event) []string {
	// Common media file extensions
	imageExtensions := []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg", ".avif"}
	videoExtensions := []string{".mp4", ".webm", ".mov", ".avi", ".mkv", ".m4v", ".ogv", ".mpg", ".mpeg"}
	mediaExtensions := append(append([]string{}, imageExtensions...), videoExtensions...)

	// Common media hosting services
	mediaHostingPatterns := []string{
		// Image hosting services
		"imgur.com",
		"nostr.build/i/",
		"nostr.build/p/",
		"image.nostr.build",
		"i.nostr.build",

		// Video hosting services
		"nostr.build/v/",
		"v.nostr.build",
		"video.nostr.build",
		"youtube.com/watch",
		"youtu.be/",
		"vimeo.com/",

		// Generic hosting
		"void.cat",
		"primal.net/",
		"pbs.twimg.com",
	}

	// URL extraction regex
	urlRegex := regexp.MustCompile(`https?://[^\s<>"']+`)

	var urls []string
	seen := make(map[string]bool) // Track seen URLs to avoid duplicates

	// Extract from content text
	contentURLs := urlRegex.FindAllString(event.Content, -1)
	for _, url := range contentURLs {
		url = strings.Split(url, "?")[0] // Remove query parameters
		urlLower := strings.ToLower(url)

		// Check for file extensions
		for _, ext := range mediaExtensions {
			if strings.HasSuffix(urlLower, ext) && !seen[url] {
				urls = append(urls, url)
				seen[url] = true
				break
			}
		}

		// Check for common media hosting services
		for _, pattern := range mediaHostingPatterns {
			if strings.Contains(urlLower, pattern) && !seen[url] {
				urls = append(urls, url)
				seen[url] = true
				break
			}
		}
	}

	// Extract from r tags (common in Nostr Build)
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "r" {
			url := tag[1]
			url = strings.Split(url, "?")[0] // Remove query parameters
			urlLower := strings.ToLower(url)

			// Check extensions
			for _, ext := range mediaExtensions {
				if strings.HasSuffix(urlLower, ext) && !seen[url] {
					urls = append(urls, url)
					seen[url] = true
					break
				}
			}

			// Check hosting services
			for _, pattern := range mediaHostingPatterns {
				if strings.Contains(urlLower, pattern) && !seen[url] {
					urls = append(urls, url)
					seen[url] = true
					break
				}
			}
		}
	}

	// Extract from imeta and vmeta tags
	for _, tag := range event.Tags {
		if len(tag) >= 2 && (tag[0] == "imeta" || tag[0] == "vmeta") {
			for _, value := range tag[1:] {
				if strings.HasPrefix(value, "url ") {
					mediaURL := strings.TrimPrefix(value, "url ")
					if !seen[mediaURL] {
						urls = append(urls, mediaURL)
						seen[mediaURL] = true
					}
				}
			}
		}
	}

	return urls
}

// For backward compatibility
func ExtractImageURLsFromEvent(event *nostr.Event) []string {
	return ExtractMediaURLsFromEvent(event)
}

// Blossom Blobs (unchunked data)
func (store *BadgerholdStore) StoreBlob(data []byte, hash []byte, publicKey string) error {
	encodedHash := hex.EncodeToString(hash)

	mtype := mimetype.Detect(data)

	content := types.BlobContent{
		Hash:    encodedHash,
		PubKey:  publicKey,
		Content: data,
	}

	err := store.Database.Upsert(encodedHash, content)
	if err != nil {
		return err
	}

	// Record file statistics
	if store.StatsDatabase != nil {
		err = store.StatsDatabase.SaveFile(
			encodedHash,      // Using hash as root
			encodedHash,      // Using hash as hash too
			"",               // No filename available for blobs
			mtype.String(),   // MIME type
			1,                // Leaf count is 1 for blobs
			int64(len(data)), // Size in bytes
		)
		if err != nil {
			// Log the error but don't fail the operation
			logging.Infof("Failed to record blob statistics: %v\n", err)
		}
	}

	return nil
}

func (store *BadgerholdStore) GetBlob(hash string) ([]byte, error) {
	var content types.BlobContent

	err := store.Database.Get(hash, &content)
	if err != nil {
		return nil, err
	}

	return content.Content, nil
}

func (store *BadgerholdStore) DeleteBlob(hash string) error {
	err := store.Database.Delete(hash, types.BlobContent{})
	if err != nil {
		return err
	}

	return nil
}

func (store *BadgerholdStore) QueryBlobs(mimeType string) ([]string, error) {

	return nil, nil
}

func GetKindFromItemName(itemName string) string {
	parts := strings.Split(itemName, ".")
	return parts[len(parts)-1]
}

func GetAppNameFromPath(path string) string {
	path = strings.TrimPrefix(path, "/")

	parts := strings.Split(path, "/")

	if len(parts) > 0 {
		return parts[0]
	}

	return ""
}

// Helper functions for dealing with event tags
func IsSingleLetter(s string) bool {
	if len(s) != 1 {
		return false
	}
	r := rune(s[0])
	return unicode.IsLower(r) && unicode.IsLetter(r)
}

func IsTagQueryTag(s string) bool {
	return len(s) == 2 && s[0] == '#' && IsSingleLetter(string(s[1]))
}

func ContainsAnyWithWildcard(tags nostr.Tags, tagName string, values []string) bool {
	tagName = strings.TrimPrefix(tagName, "#")
	for _, tag := range tags {
		if len(tag) < 2 {
			continue
		}

		if tag[0] != tagName {
			continue
		}

		for _, value := range values {
			if tagName == "f" || tagName == "d" {
				if matchWildcard(value, tag[1]) {
					return true
				}
			} else {
				if value == tag[1] {
					return true
				}
			}
		}
	}

	return false
}

func matchWildcard(pattern, value string) bool {
	patternParts := strings.Split(pattern, "/")
	valueParts := strings.Split(value, "/")

	patternIndex, valueIndex := 0, 0

	for patternIndex < len(patternParts) && valueIndex < len(valueParts) {
		switch patternParts[patternIndex] {
		case "*":
			patternIndex++
			if patternIndex == len(patternParts) {
				return true // "*" at the end matches everything remaining
			}
			// Find the next matching part
			for valueIndex < len(valueParts) && valueParts[valueIndex] != patternParts[patternIndex] {
				valueIndex++
			}
		case valueParts[valueIndex]:
			patternIndex++
			valueIndex++
		default:
			return false
		}
	}

	// Check if we've matched all parts
	return patternIndex == len(patternParts) && valueIndex == len(valueParts)
}

func ContainsAny(tags nostr.Tags, tagName string, values []string) bool {
	tagName = strings.TrimPrefix(tagName, "#")
	for _, tag := range tags {
		if len(tag) < 2 {
			continue
		}

		if tag[0] != tagName {
			continue
		}

		if slices.Contains(values, tag[1]) {
			return true
		}
	}

	return false
}

func (store *BadgerholdStore) SaveSubscriber(subscriber *types.Subscriber) error {
	// Store the subscriber data in the tree
	if err := store.Database.Upsert(subscriber.Npub, subscriber); err != nil {
		return fmt.Errorf("failed to put subscriber in Graviton store: %v", err)
	}

	return nil
}

func (store *BadgerholdStore) GetSubscriberByAddress(address string) (*types.Subscriber, error) {
	var results []types.Subscriber

	err := store.Database.Find(&results, badgerhold.Where("Address").Eq(address).Index("Address"))
	if err != nil {
		return nil, err
	}

	if len(results) > 0 {
		return &results[0], nil
	}

	return nil, fmt.Errorf("subscriber not found for address: %s", address)
}

func (store *BadgerholdStore) GetSubscriber(npub string) (*types.Subscriber, error) {
	var results []types.Subscriber

	err := store.Database.Find(&results, badgerhold.Where("Npub").Eq(npub).Index("Npub"))
	if err != nil {
		return nil, err
	}

	if len(results) > 0 {
		return &results[0], nil
	}

	// If no subscriber was found with the matching npub, return an error
	return nil, fmt.Errorf("subscriber not found for npub: %s", npub)
}

// AllocateBitcoinAddress allocates an available Bitcoin address to a subscriber.
func (store *BadgerholdStore) AllocateBitcoinAddress(npub string) (*types.Address, error) {
	var results []types.Address

	err := store.Database.Find(&results, badgerhold.Where("Status").Eq(AddressStatusAvailable).Index("Status"))
	if err != nil {
		return nil, err
	}

	if len(results) > 0 {
		addr := results[0]

		now := time.Now()
		addr.AllocatedAt = &now
		addr.Status = AddressStatusAllocated
		addr.Npub = npub

		err = store.Database.Upsert(addr.IndexHornets, addr)
		if err != nil {
			return nil, err
		}

		return &addr, nil
	}

	return nil, fmt.Errorf("no available addresses")
}

func (store *BadgerholdStore) AllocateAddress() (*types.Address, error) {
	var results []types.Address

	err := store.Database.Find(&results, badgerhold.Where("Status").Eq(AddressStatusAvailable).Index("Status"))
	if err != nil {
		return nil, err
	}

	if len(results) > 0 {
		addr := results[0]

		now := time.Now()
		addr.AllocatedAt = &now
		addr.Status = AddressStatusAllocated

		err = store.Database.Upsert(addr.IndexHornets, addr)
		if err != nil {
			return nil, err
		}

		return &addr, nil
	}

	return nil, fmt.Errorf("no available addresses")
}

func (store *BadgerholdStore) SaveAddress(addr *types.Address) error {
	err := store.Database.Upsert(addr.IndexHornets, addr)
	if err != nil {
		return fmt.Errorf("failed to put address in Graviton store: %v", err)
	}

	return nil
}
