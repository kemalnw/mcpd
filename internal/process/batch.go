package process

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	defaultBatchInitialWaitMS = 40
	defaultBatchReadTimeoutMS = 5000
	defaultBatchReadLines     = 100
)

type BatchState string

type BatchJobState string

const (
	BatchRunning   BatchState = "running"
	BatchCompleted BatchState = "completed"
	BatchCanceled  BatchState = "canceled"

	BatchJobQueued      BatchJobState = "queued"
	BatchJobRunning     BatchJobState = "running"
	BatchJobWaiting     BatchJobState = "waiting_for_input"
	BatchJobCompleted   BatchJobState = "completed"
	BatchJobFailed      BatchJobState = "failed"
	BatchJobCanceled    BatchJobState = "canceled"
	BatchJobStartFailed BatchJobState = "start_failed"
	BatchJobBlocked     BatchJobState = "blocked"
)

type BatchJobRequest struct {
	ID              string
	Command         string
	CWD             string
	Shell           string
	PTY             PTYMode
	SeparateStreams bool
	DependsOn       []string
}

type BatchStartRequest struct {
	Jobs          []BatchJobRequest
	MaxParallel   int
	InitialWaitMS int
}

type BatchReadRequest struct {
	BatchID     string
	TimeoutMS   int
	Length      int
	OnlyChanged bool
}

type BatchJobResult struct {
	ID              string        `json:"id"`
	PID             int           `json:"pid,omitempty"`
	State           BatchJobState `json:"state"`
	ExitCode        *int          `json:"exit_code,omitempty"`
	Lines           []string      `json:"lines,omitempty"`
	Streams         []StreamLine  `json:"streams,omitempty"`
	LatestLine      *StreamLine   `json:"latest_line,omitempty"`
	Generation      uint64        `json:"generation"`
	ReadFrom        int           `json:"read_from,omitempty"`
	ReadCount       int           `json:"read_count"`
	TotalLines      int           `json:"total_lines"`
	Remaining       int           `json:"remaining"`
	WaitingForInput bool          `json:"waiting_for_input"`
	RuntimeMS       int64         `json:"runtime_ms"`
	Error           string        `json:"error,omitempty"`
}

type BatchCounts struct {
	Queued    int `json:"queued"`
	Running   int `json:"running"`
	Waiting   int `json:"waiting"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Canceled  int `json:"canceled"`
	Blocked   int `json:"blocked"`
}

type BatchResult struct {
	BatchID     string           `json:"batch_id"`
	State       BatchState       `json:"state"`
	Generation  uint64           `json:"generation"`
	MaxParallel int              `json:"max_parallel"`
	Counts      BatchCounts      `json:"counts"`
	Jobs        []BatchJobResult `json:"jobs,omitempty"`
}

type BatchCancelResult struct {
	BatchID  string     `json:"batch_id"`
	State    BatchState `json:"state"`
	Canceled int        `json:"canceled"`
}

type processBatch struct {
	id          string
	maxParallel int
	createdAt   time.Time
	state       BatchState
	generation  uint64
	canceled    bool
	jobs        []*batchJob
	byID        map[string]*batchJob
	notify      chan struct{}
}

type batchJob struct {
	req                 BatchJobRequest
	session             *session
	pid                 int
	state               BatchJobState
	err                 string
	changeGeneration    uint64
	lastDeliveredChange uint64
	cursorAbs           int64
	cursorGeneration    uint64
}

func (m *Manager) StartBatch(ctx context.Context, req BatchStartRequest) (BatchResult, error) {
	if len(req.Jobs) < 2 {
		return BatchResult{}, errors.New("batch requires at least two independent jobs")
	}
	maxParallel := req.MaxParallel
	if maxParallel == 0 {
		maxParallel = m.opts.BatchMaxParallel
	}
	if maxParallel <= 0 {
		return BatchResult{}, errors.New("max_parallel must be positive")
	}
	if maxParallel > m.opts.BatchMaxParallel {
		maxParallel = m.opts.BatchMaxParallel
	}
	waitMS := req.InitialWaitMS
	if waitMS == 0 {
		waitMS = defaultBatchInitialWaitMS
	}
	if waitMS < 0 {
		return BatchResult{}, errors.New("initial_wait_ms must be >= 0")
	}
	seen := make(map[string]struct{}, len(req.Jobs))
	jobs := make([]*batchJob, 0, len(req.Jobs))
	for _, jobReq := range req.Jobs {
		jobReq.ID = strings.TrimSpace(jobReq.ID)
		if jobReq.ID == "" {
			return BatchResult{}, errors.New("every batch job requires a non-empty id")
		}
		if _, ok := seen[jobReq.ID]; ok {
			return BatchResult{}, fmt.Errorf("duplicate batch job id %q", jobReq.ID)
		}
		seen[jobReq.ID] = struct{}{}
		if strings.TrimSpace(jobReq.Command) == "" {
			return BatchResult{}, fmt.Errorf("batch job %q command is required", jobReq.ID)
		}
		if jobReq.PTY == PTYAlways {
			return BatchResult{}, fmt.Errorf("batch job %q requests PTY=always; interactive PTY jobs must use start_process", jobReq.ID)
		}
		jobs = append(jobs, &batchJob{req: jobReq, state: BatchJobQueued})
	}
	if err := validateBatchDAG(jobs); err != nil {
		return BatchResult{}, err
	}
	id, err := newBatchID()
	if err != nil {
		return BatchResult{}, err
	}
	b := &processBatch{
		id: id, maxParallel: maxParallel, createdAt: time.Now().UTC(), state: BatchRunning,
		jobs: jobs, byID: make(map[string]*batchJob, len(jobs)), notify: make(chan struct{}),
	}
	for _, job := range jobs {
		b.byID[job.req.ID] = job
	}
	m.addBatch(b)
	go m.runBatch(b)

	if waitMS > 0 {
		m.waitForBatchChange(ctx, b, 0, time.Duration(waitMS)*time.Millisecond)
	}
	return m.readBatchSnapshot(b, false, defaultBatchReadLines), nil
}

func (m *Manager) ReadBatch(ctx context.Context, req BatchReadRequest) (BatchResult, error) {
	b, err := m.getBatch(req.BatchID)
	if err != nil {
		return BatchResult{}, err
	}
	if req.TimeoutMS < 0 {
		return BatchResult{}, errors.New("timeout_ms must be >= 0")
	}
	if req.TimeoutMS == 0 {
		req.TimeoutMS = defaultBatchReadTimeoutMS
	}
	if req.Length <= 0 {
		req.Length = defaultBatchReadLines
	}
	if req.OnlyChanged {
		m.batchMu.Lock()
		baseline := b.generation
		terminal := b.state != BatchRunning
		m.batchMu.Unlock()
		if !terminal {
			m.waitForBatchChange(ctx, b, baseline, time.Duration(req.TimeoutMS)*time.Millisecond)
		}
	}
	return m.readBatchSnapshot(b, req.OnlyChanged, req.Length), nil
}

func (m *Manager) CancelBatch(batchID string) (BatchCancelResult, error) {
	b, err := m.getBatch(batchID)
	if err != nil {
		return BatchCancelResult{}, err
	}
	m.batchMu.Lock()
	if b.canceled {
		state := b.state
		m.batchMu.Unlock()
		return BatchCancelResult{BatchID: batchID, State: state}, nil
	}
	b.canceled = true
	b.state = BatchCanceled
	pids := make([]int, 0, len(b.jobs))
	canceled := 0
	for _, job := range b.jobs {
		switch job.state {
		case BatchJobQueued:
			job.state = BatchJobCanceled
			job.changeGeneration++
			canceled++
		case BatchJobRunning, BatchJobWaiting:
			job.state = BatchJobCanceled
			job.changeGeneration++
			if job.pid > 0 {
				pids = append(pids, job.pid)
			}
			canceled++
		}
	}
	m.bumpBatchLocked(b)
	m.batchMu.Unlock()
	for _, pid := range pids {
		_ = m.ForceTerminate(pid)
	}
	return BatchCancelResult{BatchID: batchID, State: BatchCanceled, Canceled: canceled}, nil
}

func (m *Manager) runBatch(b *processBatch) {
	done := make(chan *batchJob, len(b.jobs))
	running := 0
	for {
		m.batchMu.Lock()
		if b.canceled {
			for _, job := range b.jobs {
				if job.state == BatchJobQueued {
					job.state = BatchJobCanceled
					job.changeGeneration++
				}
			}
			m.bumpBatchLocked(b)
		} else {
			// Resolve permanently blocked dependents before filling available slots.
			for _, job := range b.jobs {
				if job.state != BatchJobQueued {
					continue
				}
				if reason := batchDependencyFailure(b, job); reason != "" {
					job.state = BatchJobBlocked
					job.err = reason
					job.changeGeneration++
					m.bumpBatchLocked(b)
				}
			}
			for running < b.maxParallel {
				var ready *batchJob
				for _, job := range b.jobs {
					if job.state == BatchJobQueued && batchDependenciesComplete(b, job) {
						ready = job
						break
					}
				}
				if ready == nil {
					break
				}
				// Reserve synchronously so the scheduler cannot launch the same job
				// twice while process creation is still in progress.
				ready.state = BatchJobRunning
				ready.changeGeneration++
				running++
				m.bumpBatchLocked(b)
				go func(job *batchJob) {
					m.runBatchJob(b, job)
					done <- job
				}(ready)
			}
		}
		terminal := batchTerminalCount(b)
		m.batchMu.Unlock()

		if terminal == len(b.jobs) && running == 0 {
			break
		}
		if running > 0 {
			<-done
			running--
			continue
		}
		// A valid acyclic graph with no live jobs always has either a ready root
		// or a newly blockable descendant; immediately re-evaluate those states.
	}
	m.batchMu.Lock()
	if !b.canceled {
		b.state = BatchCompleted
	}
	m.bumpBatchLocked(b)
	m.batchMu.Unlock()
	m.markBatchCompleted(b.id)
}

func batchTerminalCount(b *processBatch) int {
	count := 0
	for _, job := range b.jobs {
		switch job.state {
		case BatchJobCompleted, BatchJobFailed, BatchJobStartFailed, BatchJobCanceled, BatchJobBlocked:
			count++
		}
	}
	return count
}

func batchDependenciesComplete(b *processBatch, job *batchJob) bool {
	for _, dependency := range job.req.DependsOn {
		dep := b.byID[dependency]
		if dep == nil || dep.state != BatchJobCompleted {
			return false
		}
	}
	return true
}

func batchDependencyFailure(b *processBatch, job *batchJob) string {
	for _, dependency := range job.req.DependsOn {
		dep := b.byID[dependency]
		if dep == nil {
			return fmt.Sprintf("unknown dependency %q", dependency)
		}
		switch dep.state {
		case BatchJobFailed, BatchJobStartFailed, BatchJobCanceled, BatchJobBlocked:
			return fmt.Sprintf("dependency %q ended in state %s", dependency, dep.state)
		}
	}
	return ""
}

func validateBatchDAG(jobs []*batchJob) error {
	byID := make(map[string]*batchJob, len(jobs))
	for _, job := range jobs {
		byID[job.req.ID] = job
	}
	for _, job := range jobs {
		seenDeps := make(map[string]struct{}, len(job.req.DependsOn))
		for _, dependency := range job.req.DependsOn {
			dependency = strings.TrimSpace(dependency)
			if dependency == "" {
				return fmt.Errorf("batch job %q has an empty dependency", job.req.ID)
			}
			if dependency == job.req.ID {
				return fmt.Errorf("batch job %q depends on itself", job.req.ID)
			}
			if _, ok := byID[dependency]; !ok {
				return fmt.Errorf("batch job %q has unknown dependency %q", job.req.ID, dependency)
			}
			if _, ok := seenDeps[dependency]; ok {
				return fmt.Errorf("batch job %q repeats dependency %q", job.req.ID, dependency)
			}
			seenDeps[dependency] = struct{}{}
		}
	}
	visiting := make(map[string]bool, len(jobs))
	visited := make(map[string]bool, len(jobs))
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("batch dependency cycle includes %q", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, dependency := range byID[id].req.DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for _, job := range jobs {
		if err := visit(job.req.ID); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) runBatchJob(b *processBatch, job *batchJob) {
	s, err := m.startSession(job.req.Command, job.req.CWD, job.req.Shell, job.req.PTY, job.req.SeparateStreams)
	if err != nil {
		m.batchMu.Lock()
		if b.canceled {
			job.state = BatchJobCanceled
		} else {
			job.state = BatchJobStartFailed
			job.err = err.Error()
		}
		job.changeGeneration++
		m.bumpBatchLocked(b)
		m.batchMu.Unlock()
		return
	}
	m.batchMu.Lock()
	job.session = s
	job.pid = s.pid
	job.changeGeneration++
	m.bumpBatchLocked(b)
	m.batchMu.Unlock()

	for {
		ch := s.currentNotify()
		select {
		case <-s.done:
			m.batchMu.Lock()
			if !b.canceled && job.state != BatchJobCanceled {
				info := s.snapshot()
				if info.ExitCode != nil && *info.ExitCode == 0 {
					job.state = BatchJobCompleted
				} else {
					job.state = BatchJobFailed
				}
			}
			job.changeGeneration++
			m.bumpBatchLocked(b)
			m.batchMu.Unlock()
			return
		case <-ch:
			m.batchMu.Lock()
			if !b.canceled && job.state != BatchJobCanceled {
				info := s.snapshot()
				if info.WaitingForInput {
					job.state = BatchJobWaiting
				} else if info.ExitCode == nil {
					job.state = BatchJobRunning
				}
				job.changeGeneration++
				m.bumpBatchLocked(b)
			}
			m.batchMu.Unlock()
		}
	}
}

func (m *Manager) waitForBatchChange(ctx context.Context, b *processBatch, baseline uint64, timeout time.Duration) {
	if timeout <= 0 {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		m.batchMu.Lock()
		if b.generation > baseline || b.state != BatchRunning {
			m.batchMu.Unlock()
			return
		}
		ch := b.notify
		m.batchMu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-ch:
		}
	}
}

func (m *Manager) readBatchSnapshot(b *processBatch, onlyChanged bool, length int) BatchResult {
	m.batchMu.Lock()
	defer m.batchMu.Unlock()
	result := BatchResult{BatchID: b.id, State: b.state, Generation: b.generation, MaxParallel: b.maxParallel}
	for _, job := range b.jobs {
		result.Counts.add(job.state)
		if onlyChanged && job.changeGeneration == job.lastDeliveredChange {
			continue
		}
		jr := BatchJobResult{ID: job.req.ID, PID: job.pid, State: job.state, Error: job.err}
		if job.session != nil {
			fillBatchJobDelta(job, &jr, length)
		}
		job.lastDeliveredChange = job.changeGeneration
		result.Jobs = append(result.Jobs, jr)
	}
	sort.SliceStable(result.Jobs, func(i, j int) bool { return result.Jobs[i].ID < result.Jobs[j].ID })
	return result
}

func fillBatchJobDelta(job *batchJob, out *BatchJobResult, length int) {
	s := job.session
	s.mu.Lock()
	defer s.mu.Unlock()
	lines := s.snapshotLinesLocked()
	streams := s.snapshotStreamsLocked()
	retainedStart := s.evictedLines
	total := retainedStart + int64(len(lines))
	start := job.cursorAbs
	if start < retainedStart {
		start = retainedStart
	}
	if start > total {
		start = total
	}
	end := start + int64(length)
	if end > total {
		end = total
	}
	localStart, localEnd := int(start-retainedStart), int(end-retainedStart)
	if s.separateStreams && !s.usePTY {
		out.Streams = append([]StreamLine(nil), streams[localStart:localEnd]...)
	} else {
		out.Lines = append([]string(nil), lines[localStart:localEnd]...)
	}
	if end == start && s.outputGeneration > job.cursorGeneration {
		out.LatestLine = s.latestLineLocked()
	}
	job.cursorAbs = end
	job.cursorGeneration = s.outputGeneration
	out.ExitCode = cloneInt(s.exitCode)
	out.Generation = s.outputGeneration
	out.ReadFrom = int(start)
	out.ReadCount = int(end - start)
	out.TotalLines = int(total)
	out.Remaining = int(total - end)
	out.WaitingForInput = s.waitingForInput
	out.RuntimeMS = time.Since(s.startedAt).Milliseconds()
}

func (c *BatchCounts) add(state BatchJobState) {
	switch state {
	case BatchJobQueued:
		c.Queued++
	case BatchJobRunning:
		c.Running++
	case BatchJobWaiting:
		c.Waiting++
	case BatchJobCompleted:
		c.Completed++
	case BatchJobFailed, BatchJobStartFailed:
		c.Failed++
	case BatchJobCanceled:
		c.Canceled++
	case BatchJobBlocked:
		c.Blocked++
	}
}

func (m *Manager) addBatch(b *processBatch) {
	m.batchMu.Lock()
	m.batches[b.id] = b
	m.batchMu.Unlock()
}

func (m *Manager) getBatch(id string) (*processBatch, error) {
	m.batchMu.Lock()
	b := m.batches[id]
	m.batchMu.Unlock()
	if b == nil {
		return nil, fmt.Errorf("process batch %q not found", id)
	}
	return b, nil
}

func (m *Manager) markBatchCompleted(id string) {
	m.batchMu.Lock()
	defer m.batchMu.Unlock()
	m.completedBatches = append(m.completedBatches, id)
	for len(m.completedBatches) > m.opts.CompletedSessions {
		oldest := m.completedBatches[0]
		m.completedBatches = m.completedBatches[1:]
		delete(m.batches, oldest)
	}
}

func (m *Manager) bumpBatchLocked(b *processBatch) {
	b.generation++
	close(b.notify)
	b.notify = make(chan struct{})
}

func (m *Manager) cancelAllBatches() {
	m.batchMu.Lock()
	ids := make([]string, 0, len(m.batches))
	for id, b := range m.batches {
		if b.state == BatchRunning {
			ids = append(ids, id)
		}
	}
	m.batchMu.Unlock()
	for _, id := range ids {
		_, _ = m.CancelBatch(id)
	}
}

func newBatchID() (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate process batch id: %w", err)
	}
	return "batch_" + hex.EncodeToString(b[:]), nil
}
