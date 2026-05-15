package main

import (
	"sync"
	"testing"
)

func TestStorage_KeyMutex_SameKeyReturnsSamePointer(t *testing.T) {
	s := NewStorage(t.TempDir())
	a := s.keyMutex("b", "k")
	b := s.keyMutex("b", "k")
	if a != b {
		t.Errorf("keyMutex returned different pointers for same (bucket,key)")
	}
}

func TestStorage_KeyMutex_DifferentKeysReturnDifferentPointers(t *testing.T) {
	s := NewStorage(t.TempDir())
	a := s.keyMutex("b", "k1")
	b := s.keyMutex("b", "k2")
	if a == b {
		t.Errorf("keyMutex returned same pointer for different keys")
	}
}

func TestStorage_KeyMutex_ConcurrentLoadOrStoreReturnsSame(t *testing.T) {
	s := NewStorage(t.TempDir())
	const goroutines = 100
	var wg sync.WaitGroup
	pointers := make([]*sync.RWMutex, goroutines)
	for i := range pointers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pointers[i] = s.keyMutex("b", "k")
		}(i)
	}
	wg.Wait()
	for i := 1; i < goroutines; i++ {
		if pointers[i] != pointers[0] {
			t.Errorf("goroutine %d got different pointer than goroutine 0", i)
		}
	}
}
