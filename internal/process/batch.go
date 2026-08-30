package process

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	Cursor      string
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
	EvictedLines    int64         `json:"evicted_lines"`
	CursorEvicted   bool          `json:"cursor_evicted,omitempty"`
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
	Cursor      string           `json:"cursor"`
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
	req              BatchJobRequest
	session          *session
	pid              int
	state            BatchJobState
	err              string
	changeGeneration uint64
}

type batchCursor struct {
	Version    int                       `json:"version"`
	BatchID    string                    `json:"batch_id"`
	Generation uint64                    `json:"generation"`
	Jobs       map[string]batchJobCursor `json:"jobs"`
}

type batchJobCursor struct {
	ChangeGeneration uint64 `json:"change_generation"`
	OutputAbs        int64  `json:"output_abs"`
	OutputGeneration uint64 `json:"output_generation"`
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
	return m.readBatchSnapshot(b, false, defaultBatchReadLines, batchCursor{Version: 1, BatchID: b.id, Jobs: make(map[string]batchJobCursor)}), nil
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

	var cursor batchCursor
	if req.Cursor != "" {
		cursor, err = decodeBatchCursor(req.Cursor)
		if err != nil {
			return BatchResult{}, err
		}
		if cursor.BatchID != req.BatchID {
			return BatchResult{}, errors.New("batch cursor belongs to a different batch")
		}
	} else if req.OnlyChanged {
		// Compatibility behavior for changed-only reads without a cursor: establish
		// a baseline at call time and wait for the next change. Resumable callers
		// should first take a snapshot and then pass the returned opaque cursor.
		m.batchMu.Lock()
		cursor = m.currentBatchCursorLocked(b)
		m.batchMu.Unlock()
	} else {
		cursor = batchCursor{Version: 1, BatchID: b.id, Jobs: make(map[string]batchJobCursor)}
	}

	if req.OnlyChanged {
		m.batchMu.Lock()
		baseline := cursor.Generation
		terminal := b.state != BatchRunning
		unread := m.batchCursorHasUnreadLocked(b, cursor)
		m.batchMu.Unlock()
		if !terminal && !unread {
			m.waitForBatchChange(ctx, b, baseline, time.Duration(req.TimeoutMS)*time.Millisecond)
		}
	}
	return m.readBatchSnapshot(b, req.OnlyChanged, req.Length, cursor), nil
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

func (m *Manager) currentBatchCursorLocked(b *processBatch) batchCursor {
	cursor := batchCursor{Version: 1, BatchID: b.id, Generation: b.generation, Jobs: make(map[string]batchJobCursor, len(b.jobs))}
	for _, job := range b.jobs {
		state := batchJobCursor{ChangeGeneration: job.changeGeneration}
		if job.session != nil {
			s := job.session
			s.mu.Lock()
			state.OutputAbs = s.totalLinesLocked()
			state.OutputGeneration = s.outputGeneration
			s.mu.Unlock()
		}
		cursor.Jobs[job.req.ID] = state
	}
	return cursor
}

func (m *Manager) batchCursorHasUnreadLocked(b *processBatch, cursor batchCursor) bool {
	if b.generation > cursor.Generation {
		return true
	}
	for _, job := range b.jobs {
		state := cursor.Jobs[job.req.ID]
		if job.changeGeneration > state.ChangeGeneration {
			return true
		}
		if job.session != nil {
			s := job.session
			s.mu.Lock()
			unread := s.totalLinesLocked() > state.OutputAbs || s.outputGeneration > state.OutputGeneration
			s.mu.Unlock()
			if unread {
				return true
			}
		}
	}
	return false
}

func (m *Manager) readBatchSnapshot(b *processBatch, onlyChanged bool, length int, cursor batchCursor) BatchResult {
	m.batchMu.Lock()
	defer m.batchMu.Unlock()
	if cursor.Jobs == nil {
		cursor.Jobs = make(map[string]batchJobCursor, len(b.jobs))
	}
	nextJobs := make(map[string]batchJobCursor, len(b.jobs))
	result := BatchResult{BatchID: b.id, State: b.state, Generation: b.generation, MaxParallel: b.maxParallel}
	for _, job := range b.jobs {
		result.Counts.add(job.state)
		state := cursor.Jobs[job.req.ID]
		changed := job.changeGeneration > state.ChangeGeneration
		if job.session != nil && !changed {
			s := job.session
			s.mu.Lock()
			changed = s.totalLinesLocked() > state.OutputAbs || s.outputGeneration > state.OutputGeneration
			s.mu.Unlock()
		}
		if onlyChanged && !changed {
			nextJobs[job.req.ID] = state
			continue
		}
		jr := BatchJobResult{ID: job.req.ID, PID: job.pid, State: job.state, Error: job.err}
		if job.session != nil {
			state = fillBatchJobDelta(job, state, &jr, length)
		}
		state.ChangeGeneration = job.changeGeneration
		nextJobs[job.req.ID] = state
		result.Jobs = append(result.Jobs, jr)
	}
	cursor.Version = 1
	cursor.Generation = b.generation
	cursor.Jobs = nextJobs
	result.Cursor = encodeBatchCursor(cursor)
	sort.SliceStable(result.Jobs, func(i, j int) bool { return result.Jobs[i].ID < result.Jobs[j].ID })
	return result
}

func fillBatchJobDelta(job *batchJob, cursor batchJobCursor, out *BatchJobResult, length int) batchJobCursor {
	s := job.session
	s.mu.Lock()
	defer s.mu.Unlock()
	lines := s.snapshotLinesLocked()
	streams := s.snapshotStreamsLocked()
	retainedStart := s.evictedLines
	total := retainedStart + int64(len(lines))
	start := cursor.OutputAbs
	if start < retainedStart {
		out.CursorEvicted = true
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
	if end == start && s.outputGeneration > cursor.OutputGeneration {
		out.LatestLine = s.latestLineLocked()
	}
	cursor.OutputAbs = end
	cursor.OutputGeneration = s.outputGeneration
	out.ExitCode = cloneInt(s.exitCode)
	out.Generation = s.outputGeneration
	out.ReadFrom = int(start)
	out.ReadCount = int(end - start)
	out.TotalLines = int(total)
	out.Remaining = int(total - end)
	out.EvictedLines = s.evictedLines
	out.WaitingForInput = s.waitingForInput
	out.RuntimeMS = time.Since(s.startedAt).Milliseconds()
	return cursor
}

func encodeBatchCursor(cursor batchCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeBatchCursor(raw string) (batchCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return batchCursor{}, errors.New("invalid batch cursor encoding")
	}
	if len(data) > 1<<20 {
		return batchCursor{}, errors.New("batch cursor is too large")
	}
	var cursor batchCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.BatchID == "" {
		return batchCursor{}, errors.New("invalid batch cursor")
	}
	if cursor.Version != 1 {
		return batchCursor{}, errors.New("unsupported batch cursor version")
	}
	if cursor.Jobs == nil {
		cursor.Jobs = make(map[string]batchJobCursor)
	}
	return cursor, nil
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
