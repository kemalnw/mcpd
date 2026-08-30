package process

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

type ResourceClass string

const (
	ResourceNormal ResourceClass = "normal"
	ResourceIO     ResourceClass = "io"
	ResourceCPU    ResourceClass = "cpu"
	ResourceHeavy  ResourceClass = "heavy"
)

type HostResources struct {
	CPUs              int     `json:"cpus"`
	Load1             float64 `json:"load_1"`
	MemoryAvailableB  uint64  `json:"memory_available_bytes"`
	GlobalParallelCap int     `json:"global_parallel_cap"`
}

type weightedWaiter struct {
	weight  int
	ready   chan struct{}
	granted bool
}

type weightedLimiter struct {
	mu       sync.Mutex
	capacity int
	used     int
	waiters  []*weightedWaiter
}

func newWeightedLimiter(capacity int) *weightedLimiter {
	return &weightedLimiter{capacity: capacity}
}

// Acquire reserves the full weight atomically. Waiters are granted FIFO so a
// stream of light jobs cannot consume fragments of capacity in front of an
// older heavy waiter. Cancellation removes an ungranted waiter without leaking
// capacity; a grant that wins the race with cancellation is treated as acquired
// and the caller remains responsible for Release.
func (l *weightedLimiter) Acquire(weight int, canceled <-chan struct{}) bool {
	weight = l.normalizeWeight(weight)
	waiter := &weightedWaiter{weight: weight, ready: make(chan struct{})}

	l.mu.Lock()
	if len(l.waiters) == 0 && l.used+weight <= l.capacity {
		l.used += weight
		l.mu.Unlock()
		return true
	}
	l.waiters = append(l.waiters, waiter)
	l.mu.Unlock()

	select {
	case <-waiter.ready:
		return true
	case <-canceled:
		l.mu.Lock()
		if waiter.granted {
			l.mu.Unlock()
			return true
		}
		for i, queued := range l.waiters {
			if queued == waiter {
				copy(l.waiters[i:], l.waiters[i+1:])
				l.waiters = l.waiters[:len(l.waiters)-1]
				break
			}
		}
		l.grantWaitersLocked()
		l.mu.Unlock()
		return false
	}
}

func (l *weightedLimiter) Release(weight int) {
	weight = l.normalizeWeight(weight)
	l.mu.Lock()
	l.used -= weight
	if l.used < 0 {
		l.used = 0
	}
	l.grantWaitersLocked()
	l.mu.Unlock()
}

func (l *weightedLimiter) grantWaitersLocked() {
	for len(l.waiters) > 0 {
		waiter := l.waiters[0]
		if l.used+waiter.weight > l.capacity {
			return
		}
		l.waiters = l.waiters[1:]
		l.used += waiter.weight
		waiter.granted = true
		close(waiter.ready)
	}
}

func (l *weightedLimiter) normalizeWeight(weight int) int {
	if weight < 1 {
		return 1
	}
	if weight > l.capacity {
		return l.capacity
	}
	return weight
}

func (l *weightedLimiter) inUse() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.used
}

func (l *weightedLimiter) queued() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.waiters)
}

func hostResources() HostResources {
	out := HostResources{CPUs: runtime.NumCPU()}
	if out.CPUs < 1 {
		out.CPUs = 1
	}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			out.Load1, _ = strconv.ParseFloat(fields[0], 64)
		}
	}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 2 && fields[0] == "MemAvailable:" {
				kb, _ := strconv.ParseUint(fields[1], 10, 64)
				out.MemoryAvailableB = kb * 1024
				break
			}
		}
		_ = f.Close()
	}
	return out
}

func recommendedGlobalParallel(resources HostResources) int {
	cap := resources.CPUs
	if cap < 2 {
		cap = 2
	}
	if cap > 16 {
		cap = 16
	}
	const gib = uint64(1 << 30)
	if resources.MemoryAvailableB > 0 {
		switch {
		case resources.MemoryAvailableB < 512<<20:
			cap = 1
		case resources.MemoryAvailableB < gib && cap > 2:
			cap = 2
		}
	}
	return cap
}

func resourceWeight(class ResourceClass, resources HostResources, capacity int) int {
	weight := 1
	switch class {
	case "", ResourceNormal, ResourceIO:
		weight = 1
	case ResourceCPU:
		weight = 2
	case ResourceHeavy:
		weight = 4
	}
	if resources.Load1 > float64(resources.CPUs)*1.25 {
		weight++
	}
	if resources.MemoryAvailableB > 0 && resources.MemoryAvailableB < 512<<20 {
		weight *= 2
	}
	if weight > capacity {
		weight = capacity
	}
	if weight < 1 {
		weight = 1
	}
	return weight
}

func validResourceClass(class ResourceClass) bool {
	switch class {
	case "", ResourceNormal, ResourceIO, ResourceCPU, ResourceHeavy:
		return true
	default:
		return false
	}
}
