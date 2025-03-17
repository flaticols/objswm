# objswm

[![Go Reference](https://pkg.go.dev/badge/github.com/flaticols/objswm.svg)](https://pkg.go.dev/github.com/flaticols/objswm)
[![Go Report Card](https://goreportcard.com/badge/github.com/flaticols/objswm)](https://goreportcard.com/report/github.com/flaticols/objswm)

A memory-optimized, fast, and thread-safe object pool for Go applications.

## Features

- **Memory Efficient**: Stores objects directly without wrapper structures
- **Thread-Safe**: Fully concurrent access support with no race conditions
- **TTL Support**: Automatic time-based object expiration
- **Configurable**: Flexible sizing, blocking behavior, and timeouts
- **Generic**: Type-safe implementation using Go generics

## Installation

```bash
go get github.com/flaticols/objswm
```

## Performance

ObjectPool is designed for high-performance scenarios where object creation is expensive.

```
BenchmarkObjectGet/Small-Pool
BenchmarkObjectGet/Small-Pool-10                39542130                30.26 ns/op            0 B/op          0 allocs/op
BenchmarkObjectGet/Medium-Pool
BenchmarkObjectGet/Medium-Pool-10               40135009                29.83 ns/op            0 B/op          0 allocs/op
BenchmarkObjectGet/Large-Pool
BenchmarkObjectGet/Large-Pool-10                39074690                30.03 ns/op            0 B/op          0 allocs/op
BenchmarkObjectGet/With-TTL
BenchmarkObjectGet/With-TTL-10                  12790063                93.55 ns/op            0 B/op          0 allocs/op
BenchmarkObjectGet/Unlimited-Pool
BenchmarkObjectGet/Unlimited-Pool-10            40188382                29.67 ns/op            0 B/op          0 allocs/op
```

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/yourusername/objswm"
)

type Connection struct {
    ID int
}

func main() {
    // Create a new pool with default configuration
    pool, err := objswm.NewObjectPool(
        func() (Connection, error) {
            // Simulate expensive connection creation
            time.Sleep(10 * time.Millisecond)
            return Connection{ID: rand.Intn(1000)}, nil
        },
        objswm.DefaultPoolConfig(),
    )
    if err != nil {
        panic(err)
    }
    defer pool.Close()

    // Get an object from the pool
    ctx := context.Background()
    conn, err := pool.Get(ctx)
    if err != nil {
        panic(err)
    }

    // Use the connection
    fmt.Printf("Using connection: %d\n", conn.ID)

    // Return it to the pool when done
    pool.Put(conn)
}
```

## Configuration

ObjectPool is highly configurable to meet different use cases:

```go
config := objswm.PoolConfig{
    InitialSize:  20,     // Pre-create 20 objects
    MaxSize:      100,    // Pool will never exceed 100 objects
    MaxIdle:      50,     // Keep at most 50 idle objects
    TTL:          30 * time.Second,  // Objects expire after 30 seconds
    AllowBlock:   true,   // Get() will block when pool is empty
    BlockTimeout: 5 * time.Second,   // Wait up to 5 seconds for an object
}

pool, err := objswm.NewObjectPool(factory, config)
```

## Best Practices

1. **Right-Size Your Pool**: Set `InitialSize`, `MaxSize`, and `MaxIdle` based on your workload patterns.
2. **Use TTL for Long-Lived Pools**: Enable TTL for long-lived connections that might go stale.
3. **Clean Resource Disposal**: Ensure the pool's `Close()` method is called to properly dispose of resources.
4. **Error Handling**: Always check errors from `Get()` operations, especially with non-blocking pools.

## Advanced Usage

### Wait for Available Objects

```go
// Create a context with a specific timeout
ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
defer cancel()

// Get will respect the context deadline
obj, err := pool.Get(ctx)
if err != nil {
    if errors.Is(err, context.DeadlineExceeded) {
        // Handle timeout specifically
    }
    // Handle other errors
}
```

### Custom Object Lifecycle

For objects that require specific initialization or cleanup:

```go
factory := func() (Database, error) {
    db := Database{}
    err := db.Connect("localhost:5432")
    return db, err
}

pool, _ := objswm.NewObjectPool(factory, objswm.DefaultPoolConfig())

// When application shuts down
pool.Close() // Will release all objects
```

## How It Works

ObjectPool uses a channel-based design for thread safety and efficient blocking operations:

1. **Object Storage**: Objects are stored directly in a channel without wrappers
2. **Memory Efficiency**: TTL tracking uses unsafe pointers to avoid unnecessary allocations
3. **Concurrency**: Atomic operations and mutexes for thread-safe counting and state management
4. **Expiration**: A background goroutine periodically removes expired objects

## License

MIT License - See [LICENSE](LICENSE) for details.
