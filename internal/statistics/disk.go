package statistics

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/xenoviz/ruk/internal/state"
)

// DiskStatistics contains the optional, on-demand disk estimate. Projection
// bytes exclude symlink targets; linked target bytes count each canonical
// target's contents once; estimated bytes avoided counts repeated references.
type DiskStatistics struct {
	ProjectionBytes       int64 `json:"projectionBytes"`
	LinkedTargetBytes     int64 `json:"linkedTargetBytes"`
	EstimatedBytesAvoided int64 `json:"estimatedBytesAvoided"`
}

// DiskOptions bounds filesystem scan parallelism. Zero or negative values use
// the default. MaxConcurrency is capped even when a caller supplies a very
// large value, keeping stats from creating an unbounded worker pool.
type DiskOptions struct {
	Concurrency    int
	MaxConcurrency int
	Workers        int
}

const (
	defaultDiskConcurrency = 4
	maxDiskConcurrency     = 32
)

type projectionTask struct {
	path string
}

type projectionResult struct {
	bytes int64
	links []string
}

type linkedTarget struct {
	size       int64
	references int64
}

// MeasureDiskStatistics scans recorded dependency projections with a bounded
// worker pool. Filesystem errors are treated as missing/unreadable content,
// matching the TypeScript stats command. Cancellation is returned to the
// caller and never leaves scan workers behind.
func MeasureDiskStatistics(ctx context.Context, snapshot state.State, options ...DiskOptions) (DiskStatistics, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return DiskStatistics{}, err
	}
	workers := diskConcurrency(options)
	tasks := make(chan projectionTask)
	results := make(chan projectionResult, workers)

	var workerGroup sync.WaitGroup
	workerGroup.Add(workers)
	for index := 0; index < workers; index++ {
		go func() {
			defer workerGroup.Done()
			for task := range tasks {
				result := scanProjection(ctx, task.path)
				results <- result
			}
		}()
	}
	collectorDone := make(chan struct{})
	var projectionBytes int64
	links := make(map[string]int64)
	go func() {
		defer close(collectorDone)
		for result := range results {
			projectionBytes = addBytes(projectionBytes, result.bytes)
			for _, target := range result.links {
				links[target]++
			}
		}
	}()

	// Dispatching is deliberately performed by this bounded caller goroutine;
	// only the fixed worker set above can execute filesystem work concurrently.
dispatch:
	for _, workspace := range snapshot.Workspaces {
		projections := []string{"node_modules"}
		if key, err := state.TreeKey(workspace.Path); err == nil {
			if tree, exists := snapshot.Trees[key]; exists && tree.Projections != nil {
				projections = tree.Projections
			}
		}
		for _, projection := range projections {
			select {
			case <-ctx.Done():
				break dispatch
			case tasks <- projectionTask{path: filepath.Join(workspace.Path, projection)}:
			}
		}
	}
	close(tasks)
	workerGroup.Wait()
	close(results)
	<-collectorDone
	if err := ctx.Err(); err != nil {
		return DiskStatistics{}, err
	}

	// Resolve target contents in one pass over one global visited set. This is
	// what makes nested and repeated links count a target only once across all
	// workspaces and projections.
	visited := make(map[string]struct{})
	var linkedBytes int64
	var avoided int64
	targetKeys := make([]string, 0, len(links))
	for target := range links {
		targetKeys = append(targetKeys, target)
	}
	sort.Strings(targetKeys)
	for _, target := range targetKeys {
		references := links[target]
		if err := ctx.Err(); err != nil {
			return DiskStatistics{}, err
		}
		entry := linkedTarget{references: references}
		entry.size = entrySize(ctx, target, visited)
		linkedBytes = addBytes(linkedBytes, entry.size)
		if references > 1 {
			avoided = addBytes(avoided, multiplyBytes(entry.size, references-1))
		}
	}
	if err := ctx.Err(); err != nil {
		return DiskStatistics{}, err
	}
	return DiskStatistics{
		ProjectionBytes:       projectionBytes,
		LinkedTargetBytes:     linkedBytes,
		EstimatedBytesAvoided: avoided,
	}, nil
}

// DiskUsage is a concise alias for MeasureDiskStatistics.
func DiskUsage(ctx context.Context, snapshot state.State, options ...DiskOptions) (DiskStatistics, error) {
	return MeasureDiskStatistics(ctx, snapshot, options...)
}

func diskConcurrency(options []DiskOptions) int {
	workers := runtime.GOMAXPROCS(0)
	if workers <= 0 {
		workers = defaultDiskConcurrency
	}
	if workers > defaultDiskConcurrency {
		workers = defaultDiskConcurrency
	}
	if len(options) != 0 {
		requested := options[0].Concurrency
		if requested <= 0 {
			requested = options[0].MaxConcurrency
		}
		if requested <= 0 {
			requested = options[0].Workers
		}
		if requested > 0 {
			workers = requested
		}
	}
	if workers > maxDiskConcurrency {
		workers = maxDiskConcurrency
	}
	if workers < 1 {
		return 1
	}
	return workers
}

func scanProjection(ctx context.Context, root string) projectionResult {
	result := projectionResult{}
	if info, err := os.Lstat(root); err != nil {
		return result
	} else if info.Mode()&os.ModeSymlink != 0 {
		if real, err := canonicalTarget(root); err == nil {
			result.links = append(result.links, real)
		}
		return result
	}
	var walk func(string)
	walk = func(directory string) {
		if ctx.Err() != nil {
			return
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				return
			}
			path := filepath.Join(directory, entry.Name())
			info, err := os.Lstat(path)
			if err != nil {
				continue
			}
			if info.Mode()&os.ModeSymlink != 0 {
				real, err := canonicalTarget(path)
				if err == nil {
					result.links = append(result.links, real)
				}
				continue
			}
			if info.IsDir() {
				walk(path)
				continue
			}
			if info.Mode().IsRegular() {
				result.bytes = addBytes(result.bytes, info.Size())
			}
		}
	}
	walk(root)
	return result
}

func entrySize(ctx context.Context, entry string, visited map[string]struct{}) int64 {
	if ctx.Err() != nil {
		return 0
	}
	real, err := canonicalTarget(entry)
	if err != nil {
		return 0
	}
	if _, exists := visited[real]; exists {
		return 0
	}
	visited[real] = struct{}{}
	info, err := os.Stat(real)
	if err != nil {
		return 0
	}
	if !info.IsDir() {
		return info.Size()
	}
	entries, err := os.ReadDir(real)
	if err != nil {
		return 0
	}
	var size int64
	for _, child := range entries {
		if ctx.Err() != nil {
			return size
		}
		size = addBytes(size, entrySize(ctx, filepath.Join(real, child.Name()), visited))
	}
	return size
}

func canonicalTarget(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	real, err = filepath.Abs(filepath.Clean(real))
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		real = strings.ToLower(real)
	}
	return real, nil
}

func addBytes(left, right int64) int64 {
	if right > 0 && left > int64(^uint64(0)>>1)-right {
		return int64(^uint64(0) >> 1)
	}
	if right < 0 && left < -int64(^uint64(0)>>1)-1-right {
		return -int64(^uint64(0)>>1) - 1
	}
	return left + right
}

func multiplyBytes(value, multiplier int64) int64 {
	if value <= 0 || multiplier <= 0 {
		return 0
	}
	maxInt := int64(^uint64(0) >> 1)
	if value > maxInt/multiplier {
		return maxInt
	}
	return value * multiplier
}
