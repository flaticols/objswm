package objswm

import (
	"context"
	"sync"
	"testing"
	"time"
)

// BenchmarkObjectGet benchmarks getting objects from the pool
func BenchmarkObjectGet(b *testing.B) {
	factory := func() (testObject, error) {
		return testObject{value: 1}, nil
	}

	// Different pool configurations to benchmark
	configs := []struct {
		name       string
		poolConfig PoolConfig
	}{
		{
			name: "Small-Pool",
			poolConfig: PoolConfig{
				InitialSize: 10,
				MaxSize:     20,
			},
		},
		{
			name: "Medium-Pool",
			poolConfig: PoolConfig{
				InitialSize: 50,
				MaxSize:     100,
			},
		},
		{
			name: "Large-Pool",
			poolConfig: PoolConfig{
				InitialSize: 100,
				MaxSize:     200,
			},
		},
		{
			name: "With-TTL",
			poolConfig: PoolConfig{
				InitialSize: 50,
				MaxSize:     100,
				TTL:         time.Minute,
			},
		},
		{
			name: "Unlimited-Pool",
			poolConfig: PoolConfig{
				InitialSize: 10,
				MaxSize:     0, // Unlimited
			},
		},
	}

	ctx := context.Background()

	for _, cfg := range configs {
		b.Run(cfg.name, func(b *testing.B) {
			pool, _ := NewObjectPool(factory, cfg.poolConfig)
			defer pool.Close()

			// Run the benchmark
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				obj, _ := pool.Get(ctx)
				pool.Put(obj)
			}
		})
	}
}

// BenchmarkObjectPoolConcurrent benchmarks concurrent access to the pool
func BenchmarkObjectPoolConcurrent(b *testing.B) {
	factory := func() (testObject, error) {
		return testObject{value: 1}, nil
	}

	// Different concurrency levels to benchmark
	concurrencyLevels := []int{1, 4, 8, 16, 32, 64, 128}

	for _, concurrency := range concurrencyLevels {
		b.Run("Concurrency-"+testName(concurrency), func(b *testing.B) {
			// Create pool with enough initial objects
			pool, _ := NewObjectPool(factory, PoolConfig{
				InitialSize: concurrency / 2,
				MaxSize:     concurrency * 2,
			})
			defer pool.Close()

			ctx := context.Background()

			// Scale b.N for the number of goroutines
			opsPerGoroutine := b.N / concurrency

			// Ensure at least 1 op per goroutine
			if opsPerGoroutine < 1 {
				opsPerGoroutine = 1
			}

			b.ResetTimer()

			var wg sync.WaitGroup
			for i := 0; i < concurrency; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()

					for j := 0; j < opsPerGoroutine; j++ {
						obj, _ := pool.Get(ctx)
						// Simulate minimal work to create some contention
						// but not slow down the benchmark too much
						_ = obj.value + 1
						pool.Put(obj)
					}
				}()
			}

			wg.Wait()
		})
	}
}

// testName converts an integer to a string for benchmark names
func testName(n int) string {
	return string(rune(n + '0'))
}

// BenchmarkObjectPoolWithExpensiveCreation benchmarks pools with expensive object creation
func BenchmarkObjectPoolWithExpensiveCreation(b *testing.B) {
	// Simulate expensive object creation
	factory := func() (testObject, error) {
		time.Sleep(100 * time.Microsecond) // 0.1ms creation time
		return testObject{value: 1}, nil
	}

	// Different pool configurations
	poolSizes := []int{1, 10, 50, 100}

	ctx := context.Background()

	for _, size := range poolSizes {
		b.Run("Initial-Size-"+testName(size), func(b *testing.B) {
			pool, _ := NewObjectPool(factory, PoolConfig{
				InitialSize: size,
				MaxSize:     size * 2,
			})
			defer pool.Close()

			// Run the benchmark
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				obj, _ := pool.Get(ctx)
				pool.Put(obj)
			}
		})
	}
}

// BenchmarkCompareToNonPooled compares pooled vs non-pooled object creation
func BenchmarkCompareToNonPooled(b *testing.B) {
	// Simulate moderately expensive object creation
	createObj := func() testObject {
		// Simulate some initialization work
		time.Sleep(20 * time.Microsecond)
		return testObject{value: 1}
	}

	factory := func() (testObject, error) {
		return createObj(), nil
	}

	// First benchmark: without pool
	b.Run("Without-Pool", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			obj := createObj()
			_ = obj
		}
	})

	// Second benchmark: with pool
	b.Run("With-Pool", func(b *testing.B) {
		pool, _ := NewObjectPool(factory, PoolConfig{
			InitialSize: 10,
			MaxSize:     50,
		})
		defer pool.Close()

		ctx := context.Background()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			obj, _ := pool.Get(ctx)
			pool.Put(obj)
		}
	})
}

// BenchmarkMemoryOverhead measures memory overhead
func BenchmarkMemoryOverhead(b *testing.B) {
	// Test with different object sizes
	benchmarks := []struct {
		name      string
		createObj func() interface{}
	}{
		{
			name: "Small-Object",
			createObj: func() interface{} {
				return &testObject{value: 1}
			},
		},
		{
			name: "Medium-Object",
			createObj: func() interface{} {
				// Create a medium-sized object (~1KB)
				obj := struct {
					data [128]int64 // 128 * 8 = 1024 bytes
				}{}
				return &obj
			},
		},
		{
			name: "Large-Object",
			createObj: func() interface{} {
				// Create a larger object (~10KB)
				obj := struct {
					data [1280]int64 // 1280 * 8 = 10240 bytes
				}{}
				return &obj
			},
		},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			// Create specific factory for this object type
			factory := func() (interface{}, error) {
				return bm.createObj(), nil
			}

			// Create pool
			pool, _ := NewObjectPool(factory, PoolConfig{
				InitialSize: 1000, // Use large initial size to measure overhead
				MaxSize:     2000,
				TTL:         time.Minute, // Enable TTL tracking to measure its overhead
			})
			defer pool.Close()

			// Force GC to get more accurate memory stats
			b.ResetTimer()
			b.ReportAllocs()

			ctx := context.Background()

			for i := 0; i < b.N; i++ {
				obj, _ := pool.Get(ctx)
				pool.Put(obj)
			}
		})
	}
}

// BenchmarkDifferentPoolImplementations compares different pool configurations
func BenchmarkDifferentPoolImplementations(b *testing.B) {
	factory := func() (testObject, error) {
		return testObject{value: 1}, nil
	}

	// Different implementation strategies
	implementations := []struct {
		name       string
		poolConfig PoolConfig
	}{
		{
			name: "With-TTL",
			poolConfig: PoolConfig{
				InitialSize: 50,
				MaxSize:     100,
				TTL:         time.Minute,
			},
		},
		{
			name: "Without-TTL",
			poolConfig: PoolConfig{
				InitialSize: 50,
				MaxSize:     100,
				TTL:         0, // No TTL
			},
		},
		{
			name: "Blocking",
			poolConfig: PoolConfig{
				InitialSize: 50,
				MaxSize:     100,
				AllowBlock:  true,
			},
		},
		{
			name: "Non-Blocking",
			poolConfig: PoolConfig{
				InitialSize: 50,
				MaxSize:     100,
				AllowBlock:  false,
			},
		},
		{
			name: "Limited-Idle",
			poolConfig: PoolConfig{
				InitialSize: 50,
				MaxSize:     100,
				MaxIdle:     25, // Only keep 25 idle objects
			},
		},
	}

	ctx := context.Background()

	for _, impl := range implementations {
		b.Run(impl.name, func(b *testing.B) {
			pool, _ := NewObjectPool(factory, impl.poolConfig)
			defer pool.Close()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				obj, _ := pool.Get(ctx)
				pool.Put(obj)
			}
		})
	}
}

// BenchmarkHighContention simulates high contention scenarios
func BenchmarkHighContention(b *testing.B) {
	factory := func() (testObject, error) {
		return testObject{value: 1}, nil
	}

	// Different pool sizes relative to goroutine count
	sizeFactors := []struct {
		name       string
		sizeFactor float64 // Pool size as a fraction of goroutine count
	}{
		{name: "Undersized-0.1x", sizeFactor: 0.1},
		{name: "Undersized-0.5x", sizeFactor: 0.5},
		{name: "Equal-1x", sizeFactor: 1.0},
		{name: "Oversized-2x", sizeFactor: 2.0},
		{name: "Oversized-10x", sizeFactor: 10.0},
	}

	// Number of concurrent goroutines
	const numGoroutines = 100

	for _, sf := range sizeFactors {
		b.Run(sf.name, func(b *testing.B) {
			initialSize := int(float64(numGoroutines) * sf.sizeFactor)
			maxSize := initialSize * 2

			pool, _ := NewObjectPool(factory, PoolConfig{
				InitialSize: initialSize,
				MaxSize:     maxSize,
				AllowBlock:  true,
			})
			defer pool.Close()

			// Ensure b.N operations spread across all goroutines
			opsPerGoroutine := b.N / numGoroutines
			if opsPerGoroutine < 1 {
				opsPerGoroutine = 1
			}

			ctx := context.Background()
			var wg sync.WaitGroup

			b.ResetTimer()

			for i := 0; i < numGoroutines; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()

					for j := 0; j < opsPerGoroutine; j++ {
						obj, err := pool.Get(ctx)
						if err == nil {
							// Simulate work with high contention
							// by holding objects for varying times
							time.Sleep(time.Duration(j%5) * time.Microsecond)
							pool.Put(obj)
						}
					}
				}()
			}

			wg.Wait()
		})
	}
}
