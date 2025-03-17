// Package objswm provides a memory-optimized, fast, thread-safe object pool implementation.
package objswm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// Factory is a function that creates a new instance of the pooled object.
type Factory[T any] func() (T, error)

// PoolConfig contains configuration options for the ObjectPool.
type PoolConfig struct {
	// InitialSize is the number of objects to pre-create when the pool is initialized.
	InitialSize int

	// MaxSize is the maximum number of objects the pool can hold.
	// If set to 0, the pool has no size limit.
	MaxSize int

	// MaxIdle is the maximum number of idle objects to keep in the pool.
	// If set to 0, all idle objects are kept until MaxSize is reached.
	MaxIdle int

	// TTL is the time-to-live for an idle object before it's removed from the pool.
	// If set to 0, objects don't expire.
	TTL time.Duration

	// AllowBlock determines if Get() should block when the pool is empty.
	// If false, Get() will return an error when no objects are available.
	AllowBlock bool

	// BlockTimeout is the maximum time to wait for an object to become available.
	// If set to 0 and AllowBlock is true, Get() will wait indefinitely.
	BlockTimeout time.Duration
}

// DefaultPoolConfig returns a default configuration for ObjectPool.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		InitialSize:  10,
		MaxSize:      100,
		MaxIdle:      50,
		TTL:          5 * time.Minute,
		AllowBlock:   true,
		BlockTimeout: 30 * time.Second,
	}
}

// ObjectPool is a memory-optimized, fast, thread-safe pool of objects.
type ObjectPool[T any] struct {
	// Configuration
	config PoolConfig

	// Object factory
	factory Factory[T]

	// Pool state - direct object storage without wrappers
	objects  chan T
	mu       sync.RWMutex
	count    int32
	shutdown atomic.Bool

	// Memory-efficient TTL tracking (only used if TTL > 0)
	ttlTracker *ttlTracker[T]
}

// ttlTracker is a memory-efficient way to track object creation times
// Only created when TTL is enabled
type ttlTracker[T any] struct {
	sync.Mutex
	cleanupTicker *time.Ticker
	stopCh        chan struct{}
	objects       map[uintptr]time.Time
}

// NewObjectPool creates a new thread-safe object pool.
func NewObjectPool[T any](factory Factory[T], config PoolConfig) (*ObjectPool[T], error) {
	if factory == nil {
		return nil, errors.New("factory function cannot be nil")
	}

	// Initialize with default config if necessary
	if config.InitialSize <= 0 {
		config.InitialSize = DefaultPoolConfig().InitialSize
	}
	if config.MaxSize <= 0 {
		config.MaxSize = DefaultPoolConfig().MaxSize
	}
	if config.MaxIdle < 0 {
		config.MaxIdle = DefaultPoolConfig().MaxIdle
	}
	if config.MaxIdle > config.MaxSize && config.MaxSize > 0 {
		config.MaxIdle = config.MaxSize
	}

	// Create the channel capacity based on configuration
	// This avoids over-allocation
	var capacity int
	if config.MaxIdle > 0 {
		capacity = config.MaxIdle // Only allocate what we'll keep idle
	} else if config.MaxSize > 0 {
		capacity = config.MaxSize
	} else {
		capacity = config.InitialSize * 2 // Unbounded, but start with a reasonable capacity
	}

	pool := &ObjectPool[T]{
		config:  config,
		factory: factory,
		objects: make(chan T, capacity),
	}

	// Only create TTL tracker if needed
	if config.TTL > 0 {
		pool.ttlTracker = &ttlTracker[T]{
			objects:       make(map[uintptr]time.Time),
			stopCh:        make(chan struct{}),
			cleanupTicker: time.NewTicker(config.TTL / 2),
		}

		// Start cleaner goroutine
		go pool.ttlTracker.cleaner(pool)
	}

	// Pre-populate pool with initial objects
	for range config.InitialSize {
		obj, err := factory()
		if err != nil {
			// If we fail to create an object, close the pool and return an error
			pool.Close()
			return nil, errors.New("failed to initialize pool: " + err.Error())
		}

		// Add object to pool and increment counter
		pool.objects <- obj
		atomic.AddInt32(&pool.count, 1)

		// Track TTL if enabled
		if pool.ttlTracker != nil {
			pool.ttlTracker.track(obj)
		}
	}

	return pool, nil
}

// track adds an object to TTL tracking
func (t *ttlTracker[T]) track(obj T) {
	if t == nil {
		return
	}

	// Use uintptr for stable tracking
	ptr := uintptr(unsafe.Pointer(&obj))

	t.Lock()
	defer t.Unlock()
	t.objects[ptr] = time.Now()
}

// untrack removes an object from TTL tracking
func (t *ttlTracker[T]) untrack(obj T) {
	if t == nil {
		return
	}

	ptr := uintptr(unsafe.Pointer(&obj))

	t.Lock()
	defer t.Unlock()
	delete(t.objects, ptr)
}

// isExpired checks if an object has exceeded its TTL
func (t *ttlTracker[T]) isExpired(obj T, ttl time.Duration) bool {
	if t == nil || ttl <= 0 {
		return false
	}

	ptr := uintptr(unsafe.Pointer(&obj))

	t.Lock()
	defer t.Unlock()

	createTime, exists := t.objects[ptr]
	if !exists {
		// If we don't have a record for this object, assume it's new
		// Add it to tracking with current time
		t.objects[ptr] = time.Now()
		return false
	}

	return time.Since(createTime) > ttl
}

// cleaner periodically removes expired objects from the pool
func (t *ttlTracker[T]) cleaner(pool *ObjectPool[T]) {
	defer t.cleanupTicker.Stop()

	// Run an initial cleanup after a short delay
	// This helps with tests that need to verify TTL functionality
	time.AfterFunc(pool.config.TTL/2, func() {
		select {
		case <-t.stopCh:
			return // Pool already closed
		default:
			pool.removeExpired()
		}
	})

	for {
		select {
		case <-t.cleanupTicker.C:
			pool.removeExpired()
		case <-t.stopCh:
			return
		}
	}
}

// Get retrieves an object from the pool.
// If the pool is empty:
//   - If AllowBlock is true, it waits until an object is available or BlockTimeout is reached
//   - If AllowBlock is false, it creates a new object if below MaxSize, or returns an error
func (p *ObjectPool[T]) Get(ctx context.Context) (T, error) {
	var zero T

	// Check if pool is closed
	if p.shutdown.Load() {
		return zero, errors.New("pool has been closed")
	}

	// Try to get from pool first
	select {
	case obj, ok := <-p.objects:
		if !ok {
			return zero, errors.New("pool has been closed")
		}

		// If using TTL and object is expired, discard and create new
		if p.ttlTracker != nil && p.ttlTracker.isExpired(obj, p.config.TTL) {
			p.ttlTracker.untrack(obj)
			atomic.AddInt32(&p.count, -1)

			// Create a new object instead
			newObj, err := p.factory()
			if err != nil {
				return zero, err
			}

			if p.ttlTracker != nil {
				p.ttlTracker.track(newObj)
			}
			return newObj, nil
		}

		return obj, nil
	default:
		// Pool is empty, handle according to configuration
	}

	// If we can create a new object (below MaxSize or unlimited)
	curCount := atomic.LoadInt32(&p.count)
	if p.config.MaxSize <= 0 || int(curCount) < p.config.MaxSize {
		obj, err := p.factory()
		if err == nil {
			atomic.AddInt32(&p.count, 1)
			if p.ttlTracker != nil {
				p.ttlTracker.track(obj)
			}
			return obj, nil
		}
		// If we couldn't create a new object, fall back to waiting if allowed
	}

	// If we can't create a new object and blocking is allowed
	if p.config.AllowBlock {
		// Determine timeout to use
		var timeoutCtx context.Context
		var cancel context.CancelFunc

		if p.config.BlockTimeout > 0 {
			// Use BlockTimeout if specified
			timeoutCtx, cancel = context.WithTimeout(ctx, p.config.BlockTimeout)
			defer cancel()
		} else {
			// Otherwise use the provided context
			timeoutCtx = ctx
		}

		// Wait for an object to become available
		select {
		case obj, ok := <-p.objects:
			if !ok {
				return zero, errors.New("pool has been closed")
			}

			// Check if object is expired (if TTL tracking enabled)
			if p.ttlTracker != nil && p.ttlTracker.isExpired(obj, p.config.TTL) {
				p.ttlTracker.untrack(obj)
				atomic.AddInt32(&p.count, -1)

				// Create a new object instead
				newObj, err := p.factory()
				if err != nil {
					return zero, err
				}

				if p.ttlTracker != nil {
					p.ttlTracker.track(newObj)
				}
				return newObj, nil
			}

			return obj, nil
		case <-timeoutCtx.Done():
			if ctx.Err() == context.Canceled {
				return zero, errors.New("context canceled")
			}
			return zero, errors.New("timeout waiting for object")
		}
	}

	// If we reach here, the pool is exhausted and blocking is not allowed
	return zero, errors.New("pool exhausted")
}

// Put returns an object to the pool.
// If the pool is full or closed, the object may be discarded.
func (p *ObjectPool[T]) Put(obj T) {
	// Check if pool is closed
	if p.shutdown.Load() {
		// If pool is closed, object is discarded
		if p.ttlTracker != nil {
			p.ttlTracker.untrack(obj)
		}
		atomic.AddInt32(&p.count, -1)
		return
	}

	// Check if object is expired (if TTL tracking enabled)
	if p.ttlTracker != nil && p.ttlTracker.isExpired(obj, p.config.TTL) {
		p.ttlTracker.untrack(obj)
		atomic.AddInt32(&p.count, -1)
		return
	}

	// Determine if we should put the object back in the pool
	currentIdle := len(p.objects)
	if p.config.MaxIdle > 0 && currentIdle >= p.config.MaxIdle {
		// We have too many idle objects, discard this one
		if p.ttlTracker != nil {
			p.ttlTracker.untrack(obj)
		}
		atomic.AddInt32(&p.count, -1)
		return
	}

	// Try to return to pool, discard if full
	select {
	case p.objects <- obj:
		// Object successfully returned to pool
		// TTL already tracked, nothing to do
	default:
		// Pool channel is full, discard object
		if p.ttlTracker != nil {
			p.ttlTracker.untrack(obj)
		}
		atomic.AddInt32(&p.count, -1)
	}
}

// Size returns the current total size of the pool (in use + idle).
func (p *ObjectPool[T]) Size() int {
	return int(atomic.LoadInt32(&p.count))
}

// IdleCount returns the number of idle objects in the pool.
func (p *ObjectPool[T]) IdleCount() int {
	return len(p.objects)
}

// Close shuts down the pool and discards all objects.
func (p *ObjectPool[T]) Close() {
	// Use atomic operation to avoid needing a sync.Once
	if !p.shutdown.CompareAndSwap(false, true) {
		return // Already closed
	}

	// Stop the TTL tracker if it exists
	if p.ttlTracker != nil {
		close(p.ttlTracker.stopCh)
		p.ttlTracker.cleanupTicker.Stop()

		// Clear the TTL tracking map
		p.ttlTracker.Lock()
		p.ttlTracker.objects = nil // Release memory
		p.ttlTracker.Unlock()
	}

	// Reset count to ensure it's properly updated regardless of objects in use
	atomic.StoreInt32(&p.count, 0)

	// Close the channel (after count is reset to avoid race conditions)
	close(p.objects)

	// Drain the channel
	for range p.objects {
		// Just drain, count is already reset
	}
}

// removeExpired removes objects that have exceeded their TTL.
func (p *ObjectPool[T]) removeExpired() {
	if p.ttlTracker == nil || p.config.TTL <= 0 {
		return
	}

	// Lock the TTL tracker during the whole operation to ensure consistency
	p.ttlTracker.Lock()
	defer p.ttlTracker.Unlock()

	// Get the current time once
	now := time.Now()

	// Temporary storage for non-expired objects
	tempObjects := make([]T, 0, len(p.objects))

	// Drain channel completely
	idleCount := len(p.objects)
	for range idleCount {
		select {
		case obj, ok := <-p.objects:
			if !ok {
				return // Channel closed
			}

			ptr := uintptr(unsafe.Pointer(&obj))

			createTime, exists := p.ttlTracker.objects[ptr]
			isExpired := exists && now.Sub(createTime) > p.config.TTL

			if isExpired {
				// Object expired, discard it
				delete(p.ttlTracker.objects, ptr)
				atomic.AddInt32(&p.count, -1)
			} else {
				// Object still valid, keep it
				tempObjects = append(tempObjects, obj)

				// Update timestamp if it doesn't exist
				if !exists {
					p.ttlTracker.objects[ptr] = now
				}
			}
		default:
			// Channel is now empty, exit loop
			goto putBack
		}
	}

putBack:
	// Put non-expired objects back
	for _, obj := range tempObjects {
		select {
		case p.objects <- obj:
			// Successfully returned
		default:
			// Pool is full again, discard
			ptr := uintptr(unsafe.Pointer(&obj))
			delete(p.ttlTracker.objects, ptr)
			atomic.AddInt32(&p.count, -1)
		}
	}
}
