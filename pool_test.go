package objswm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Simple test object
type testObject struct {
	value int
}

// TestObjectPoolCreation tests the creation of an object pool
func TestObjectPoolCreation(t *testing.T) {
	// Test with valid factory
	factory := func() (testObject, error) {
		return testObject{value: 1}, nil
	}

	config := DefaultPoolConfig()
	pool, err := NewObjectPool(factory, config)

	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}

	if pool.Size() != config.InitialSize {
		t.Errorf("Expected initial size %d, got %d", config.InitialSize, pool.Size())
	}

	if pool.IdleCount() != config.InitialSize {
		t.Errorf("Expected idle count %d, got %d", config.InitialSize, pool.IdleCount())
	}

	// Test with failing factory
	failingFactory := func() (testObject, error) {
		return testObject{}, errors.New("factory error")
	}

	_, err = NewObjectPool(failingFactory, config)
	if err == nil {
		t.Errorf("Expected error with failing factory, got nil")
	}

	// Test with nil factory
	_, err = NewObjectPool[testObject](nil, config)
	if err == nil {
		t.Errorf("Expected error with nil factory, got nil")
	}
}

// TestObjectPoolGetPut tests getting and putting objects
func TestObjectPoolGetPut(t *testing.T) {
	factory := func() (testObject, error) {
		return testObject{value: 1}, nil
	}

	config := DefaultPoolConfig()
	pool, _ := NewObjectPool(factory, config)
	defer pool.Close()

	ctx := context.Background()

	// Get an object
	obj, err := pool.Get(ctx)
	if err != nil {
		t.Fatalf("Failed to get object: %v", err)
	}

	// Verify idle count decreased
	if pool.IdleCount() != config.InitialSize-1 {
		t.Errorf("Expected idle count %d, got %d", config.InitialSize-1, pool.IdleCount())
	}

	// Put the object back
	pool.Put(obj)

	// Verify idle count increased
	if pool.IdleCount() != config.InitialSize {
		t.Errorf("Expected idle count %d, got %d", config.InitialSize, pool.IdleCount())
	}
}

// TestObjectPoolMaxSize tests the max size constraint
func TestObjectPoolMaxSize(t *testing.T) {
	factory := func() (testObject, error) {
		return testObject{value: 1}, nil
	}

	// Set small max size to test constraint
	config := PoolConfig{
		InitialSize: 1,
		MaxSize:     3,
		AllowBlock:  false, // Don't block, return error
	}

	pool, _ := NewObjectPool(factory, config)
	defer pool.Close()

	ctx := context.Background()

	// Get all objects from the pool and one extra (up to MaxSize)
	var objects []testObject
	for i := 0; i < config.MaxSize; i++ {
		obj, err := pool.Get(ctx)
		if err != nil {
			t.Fatalf("Failed to get object %d: %v", i, err)
		}
		objects = append(objects, obj)
	}

	// Pool should now be empty but at max size
	if pool.Size() != config.MaxSize {
		t.Errorf("Expected size %d, got %d", config.MaxSize, pool.Size())
	}

	if pool.IdleCount() != 0 {
		t.Errorf("Expected idle count 0, got %d", pool.IdleCount())
	}

	// Try to get one more object, should fail
	_, err := pool.Get(ctx)
	if err == nil {
		t.Errorf("Expected error when pool exhausted, got nil")
	}

	// Return all objects to the pool
	for _, obj := range objects {
		pool.Put(obj)
	}

	// Verify all objects returned
	if pool.IdleCount() != config.MaxSize {
		t.Errorf("Expected idle count %d, got %d", config.MaxSize, pool.IdleCount())
	}
}

// TestObjectPoolMaxIdle tests the max idle constraint
func TestObjectPoolMaxIdle(t *testing.T) {
	factory := func() (testObject, error) {
		return testObject{value: 1}, nil
	}

	// Set small max idle to test constraint
	config := PoolConfig{
		InitialSize: 1,
		MaxSize:     5,
		MaxIdle:     2, // Only keep 2 idle objects
	}

	pool, _ := NewObjectPool(factory, config)
	defer pool.Close()

	ctx := context.Background()

	// Get 3 objects
	var objects []testObject
	for i := 0; i < 3; i++ {
		obj, _ := pool.Get(ctx)
		objects = append(objects, obj)
	}

	// Return all objects to the pool
	for _, obj := range objects {
		pool.Put(obj)
	}

	// Verify only MaxIdle objects kept
	if pool.IdleCount() != config.MaxIdle {
		t.Errorf("Expected idle count %d, got %d", config.MaxIdle, pool.IdleCount())
	}

	// Verify total size reduced
	if pool.Size() != config.MaxIdle {
		t.Errorf("Expected size %d, got %d", config.MaxIdle, pool.Size())
	}
}

// TestObjectPoolTTL tests object expiration
func TestObjectPoolTTL(t *testing.T) {
	factory := func() (testObject, error) {
		return testObject{value: 1}, nil
	}

	// Set very short TTL for testing
	config := PoolConfig{
		InitialSize: 2,
		TTL:         10 * time.Millisecond, // Very short TTL
	}

	pool, _ := NewObjectPool(factory, config)

	// Wait to ensure TTL expires (much longer than the TTL)
	time.Sleep(50 * time.Millisecond)

	// Force multiple cleanup calls to ensure expiration
	pool.removeExpired()
	pool.removeExpired() // Second call to be extra sure

	// Check size - should be 0 or at most 1 after expiration
	size := pool.Size()
	if size > 1 {
		t.Errorf("Expected at most 1 object after TTL expiration, got %d", size)
	}

	// Clean up
	pool.Close()
}

// TestObjectPoolBlocking tests blocking behavior
func TestObjectPoolBlocking(t *testing.T) {
	factory := func() (testObject, error) {
		return testObject{value: 1}, nil
	}

	// Configure pool to block with timeout
	config := PoolConfig{
		InitialSize:  1,
		MaxSize:      1,
		AllowBlock:   true,
		BlockTimeout: 50 * time.Millisecond,
	}

	pool, _ := NewObjectPool(factory, config)
	defer pool.Close()

	ctx := context.Background()

	// Get the only object
	obj, _ := pool.Get(ctx)

	// Try to get another object in a goroutine with timeout
	errCh := make(chan error)

	go func() {
		_, err := pool.Get(ctx)
		errCh <- err
	}()

	// Wait for timeout
	select {
	case err := <-errCh:
		if err == nil {
			t.Errorf("Expected timeout error, got nil")
		}
	case <-time.After(100 * time.Millisecond):
		t.Errorf("Didn't get timeout error within expected time")
	}

	// Now return the object
	pool.Put(obj)

	// Try again, should succeed quickly
	go func() {
		obj, err := pool.Get(ctx)
		if err != nil {
			errCh <- err
		} else {
			pool.Put(obj)
			errCh <- nil
		}
	}()

	// Should succeed
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Expected success, got error: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Errorf("Didn't get object within expected time")
	}
}

// TestObjectPoolClose tests pool closure
func TestObjectPoolClose(t *testing.T) {
	factory := func() (testObject, error) {
		return testObject{value: 1}, nil
	}

	config := DefaultPoolConfig()
	pool, _ := NewObjectPool(factory, config)

	// Close the pool
	pool.Close()

	// Verify size is 0 after close
	if size := pool.Size(); size != 0 {
		t.Errorf("Expected size 0 after close, got %d", size)
	}

	// Try to get an object, should fail
	ctx := context.Background()
	_, err := pool.Get(ctx)
	if err == nil {
		t.Errorf("Expected error after pool close, got nil")
	}
}

// TestObjectPoolConcurrentAccess tests concurrent access to the pool
func TestObjectPoolConcurrentAccess(t *testing.T) {
	factory := func() (testObject, error) {
		return testObject{value: 1}, nil
	}

	config := PoolConfig{
		InitialSize: 5,
		MaxSize:     20,
		AllowBlock:  true,
	}

	pool, _ := NewObjectPool(factory, config)
	defer pool.Close()

	ctx := context.Background()

	// Run concurrent goroutines accessing the pool
	const numGoroutines = 100
	const numOperations = 10

	var wg sync.WaitGroup
	errCh := make(chan error, numGoroutines*numOperations)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for j := 0; j < numOperations; j++ {
				obj, err := pool.Get(ctx)
				if err != nil {
					errCh <- err
					continue
				}

				// Simulate some work
				time.Sleep(time.Millisecond)

				// Return object
				pool.Put(obj)
			}
		}()
	}

	// Wait for all goroutines to finish
	wg.Wait()
	close(errCh)

	// Check for errors
	errCount := 0
	for err := range errCh {
		t.Errorf("Got error during concurrent access: %v", err)
		errCount++
		if errCount >= 10 {
			t.Errorf("Too many errors, stopping report")
			break
		}
	}

	// Verify pool is still functional
	obj, err := pool.Get(ctx)
	if err != nil {
		t.Errorf("Pool not functional after concurrent access: %v", err)
	} else {
		pool.Put(obj)
	}
}

// TestObjectPoolContextCancel tests context cancellation
func TestObjectPoolContextCancel(t *testing.T) {
	factory := func() (testObject, error) {
		return testObject{value: 1}, nil
	}

	config := PoolConfig{
		InitialSize: 1,
		MaxSize:     1,
		AllowBlock:  true,
	}

	pool, _ := NewObjectPool(factory, config)
	defer pool.Close()

	// Get the only object
	ctx := context.Background()
	obj, _ := pool.Get(ctx)

	// Try to get another object with a cancelable context
	cancelCtx, cancel := context.WithCancel(ctx)

	errCh := make(chan error)
	go func() {
		_, err := pool.Get(cancelCtx)
		errCh <- err
	}()

	// Cancel the context
	time.Sleep(10 * time.Millisecond)
	cancel()

	// Should get a cancellation error
	select {
	case err := <-errCh:
		if err == nil {
			t.Errorf("Expected context canceled error, got nil")
		}
	case <-time.After(100 * time.Millisecond):
		t.Errorf("Didn't get cancellation error within expected time")
	}

	// Return the object
	pool.Put(obj)
}
