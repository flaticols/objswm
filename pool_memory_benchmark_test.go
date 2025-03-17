package objswm

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

// MemoryStats stores memory usage statistics
type MemoryStats struct {
	HeapAllocBefore uint64
	HeapAllocAfter  uint64
	HeapObjects     uint64
}

// getMemoryUsage returns current heap memory usage
func getMemoryUsage() MemoryStats {
	var stats runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&stats)
	return MemoryStats{
		HeapAllocBefore: stats.HeapAlloc,
		HeapObjects:     stats.HeapObjects,
	}
}

// measureMemoryOverhead measures memory overhead of the pool
func measureMemoryOverhead[T any](
	name string,
	poolSize int,
	factory Factory[T],
	enableTTL bool,
) {
	// Create an initial stats capture
	statsBefore := getMemoryUsage()

	// Configure pool based on parameters
	config := PoolConfig{
		InitialSize: poolSize,
		MaxSize:     poolSize * 2,
		TTL:         0,
	}

	if enableTTL {
		config.TTL = time.Minute
	}

	// Create the pool
	pool, _ := NewObjectPool(factory, config)

	// Get memory stats after pool creation
	statsAfter := getMemoryUsage()
	statsAfter.HeapAllocBefore = statsBefore.HeapAllocBefore

	// Update after value
	runtime.ReadMemStats(&runtime.MemStats{})
	statsAfter.HeapAllocAfter = runtime.MemStats{}.HeapAlloc

	// Calculate overhead
	overheadBytes := int64(statsAfter.HeapAllocAfter - statsAfter.HeapAllocBefore)
	overheadPerObject := float64(overheadBytes) / float64(poolSize)

	// Print results
	fmt.Printf("Memory Benchmark: %s\n", name)
	fmt.Printf("  Pool Size: %d objects\n", poolSize)
	fmt.Printf("  TTL Tracking: %v\n", enableTTL)
	fmt.Printf("  Total Overhead: %d bytes\n", overheadBytes)
	fmt.Printf("  Overhead Per Object: %.2f bytes\n", overheadPerObject)
	fmt.Printf("  Heap Objects: %d\n", statsAfter.HeapObjects)
	fmt.Println()

	// Clean up
	pool.Close()
}

// TestMemoryOverheadWithDifferentSizes measures memory overhead with different pool sizes
func TestMemoryOverheadWithDifferentSizes(t *testing.T) {
	// Skip in normal testing, run explicitly when needed
	if testing.Short() {
		t.Skip("Skipping memory overhead test in short mode")
	}

	// Small object factory
	smallFactory := func() (int, error) {
		return 42, nil
	}

	// Medium object factory (~100 bytes)
	mediumFactory := func() (struct {
		values [25]int32 // 25 * 4 = 100 bytes
	}, error) {
		return struct{ values [25]int32 }{}, nil
	}

	// Large object factory (~1000 bytes)
	largeFactory := func() (struct {
		values [250]int32 // 250 * 4 = 1000 bytes
	}, error) {
		return struct{ values [250]int32 }{}, nil
	}

	// Test different pool sizes
	poolSizes := []int{100, 1000, 10000}

	for _, size := range poolSizes {
		// Test with small objects
		measureMemoryOverhead("Small Objects", size, smallFactory, false)
		measureMemoryOverhead("Small Objects with TTL", size, smallFactory, true)

		// Test with medium objects
		measureMemoryOverhead("Medium Objects", size, mediumFactory, false)
		measureMemoryOverhead("Medium Objects with TTL", size, mediumFactory, true)

		// Only test large objects with smaller pool sizes
		if size <= 1000 {
			measureMemoryOverhead("Large Objects", size, largeFactory, false)
			measureMemoryOverhead("Large Objects with TTL", size, largeFactory, true)
		}
	}
}

// runMemoryLeakTest runs a test to check for memory leaks
func runMemoryLeakTest(t *testing.T, iterations int, useWithTTL bool) {
	factory := func() (int, error) {
		return 42, nil
	}

	config := PoolConfig{
		InitialSize: 100,
		MaxSize:     1000,
		MaxIdle:     500,
		TTL:         0,
	}

	if useWithTTL {
		config.TTL = time.Minute
	}

	pool, _ := NewObjectPool(factory, config)
	defer pool.Close()

	ctx := context.Background()

	// Capture memory stats before test
	var statsBefore runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&statsBefore)

	// Run many get/put cycles
	var wg sync.WaitGroup
	numGoroutines := 8

	opsPerGoroutine := iterations / numGoroutines

	for range numGoroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for range opsPerGoroutine {
				obj, _ := pool.Get(ctx)
				time.Sleep(time.Microsecond) // Tiny delay to simulate work
				pool.Put(obj)
			}
		}()
	}

	wg.Wait()

	// Force cleanup
	for range 5 {
		runtime.GC()
	}

	// Capture memory stats after test
	var statsAfter runtime.MemStats
	runtime.ReadMemStats(&statsAfter)

	// Check if memory usage has grown abnormally
	// This is just an approximation, not a precise test
	memRatio := float64(statsAfter.HeapAlloc) / float64(statsBefore.HeapAlloc)
	objectRatio := float64(statsAfter.HeapObjects) / float64(statsBefore.HeapObjects)

	fmt.Printf("Memory Leak Test (%s):\n", map[bool]string{true: "With TTL", false: "Without TTL"}[useWithTTL])
	fmt.Printf("  Before - HeapAlloc: %d, HeapObjects: %d\n", statsBefore.HeapAlloc, statsBefore.HeapObjects)
	fmt.Printf("  After  - HeapAlloc: %d, HeapObjects: %d\n", statsAfter.HeapAlloc, statsAfter.HeapObjects)
	fmt.Printf("  Ratio  - HeapAlloc: %.2f, HeapObjects: %.2f\n", memRatio, objectRatio)

	// Only fail the test if there's a significant leak
	// Some variation is normal due to Go's GC behavior
	if memRatio > 1.5 && statsAfter.HeapAlloc-statsBefore.HeapAlloc > 1024*1024 {
		t.Errorf("Possible memory leak detected: HeapAlloc ratio %.2f", memRatio)
	}

	if objectRatio > 1.5 && statsAfter.HeapObjects-statsBefore.HeapObjects > 10000 {
		t.Errorf("Possible object leak detected: HeapObjects ratio %.2f", objectRatio)
	}
}

// TestMemoryLeaks tests for memory leaks over time
func TestMemoryLeaks(t *testing.T) {
	// Skip in normal testing, run explicitly when needed
	if testing.Short() {
		t.Skip("Skipping memory leak test in short mode")
	}

	// About 1 million operations
	iterations := 1000000

	// Test without TTL tracking
	runMemoryLeakTest(t, iterations, false)

	// Test with TTL tracking
	runMemoryLeakTest(t, iterations, true)
}

// BenchmarkMemoryFootprint compares memory footprint of pool vs. no-pool
func BenchmarkMemoryFootprint(b *testing.B) {
	// Create a medium-sized object
	type benchObject struct {
		id    int
		value [10]int64 // 80 bytes
	}

	// Define factory
	factory := func() (benchObject, error) {
		return benchObject{id: 1}, nil
	}

	// First benchmark: with pool reuse
	b.Run("With-Pool", func(b *testing.B) {
		pool, _ := NewObjectPool(factory, PoolConfig{
			InitialSize: 100,
			MaxSize:     1000,
		})
		defer pool.Close()

		ctx := context.Background()

		b.ResetTimer()
		b.ReportAllocs()

		for b.Loop() {
			obj, _ := pool.Get(ctx)
			_ = obj.id // Use object
			pool.Put(obj)
		}
	})

	// Second benchmark: without pool reuse
	b.Run("Without-Pool", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()

		for b.Loop() {
			obj, _ := factory()
			_ = obj.id // Use object
		}
	})
}
