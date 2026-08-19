package cli

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/state"
)

type capturingWorktreeRecorder struct {
	mu        sync.Mutex
	records   []capturedWorktreeRecord
	forgets   []string
	recordErr error
	forgetErr error
}

type capturedWorktreeRecord struct {
	path, branch, source string
}

func (recorder *capturingWorktreeRecorder) RecordWorktree(_ context.Context, path, branch, source string) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.records = append(recorder.records, capturedWorktreeRecord{path: path, branch: branch, source: source})
	return recorder.recordErr
}

func (recorder *capturingWorktreeRecorder) ForgetWorktree(_ context.Context, path string) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.forgets = append(recorder.forgets, path)
	return recorder.forgetErr
}

type trackingCreateWorkspaceStub struct {
	created   bool
	removed   bool
	force     bool
	path      string
	createErr error
	removeErr error
}

func (stub *trackingCreateWorkspaceStub) Create(_ context.Context, path, _, _ string, _ bool) error {
	stub.created = true
	stub.path = path
	return stub.createErr
}

func (stub *trackingCreateWorkspaceStub) Remove(_ context.Context, path string, force bool) error {
	stub.removed = true
	stub.force = force
	stub.path = path
	return stub.removeErr
}

type acquisitionWorktreeCore struct {
	created  []string
	assigned []string
	locked   []string
}

func (core *acquisitionWorktreeCore) Create(_ context.Context, path, branch, start string) error {
	core.created = append(core.created, strings.Join([]string{path, branch, start}, "|"))
	return nil
}

func (core *acquisitionWorktreeCore) Lock(_ context.Context, path string) error {
	core.locked = append(core.locked, path)
	return nil
}

func (core *acquisitionWorktreeCore) Assign(_ context.Context, path, branch, start string) error {
	core.assigned = append(core.assigned, strings.Join([]string{path, branch, start}, "|"))
	return nil
}

func TestRecordingCreateWorkspaceRecordsAfterSuccessfulCreate(t *testing.T) {
	inner := &trackingCreateWorkspaceStub{}
	recorder := &capturingWorktreeRecorder{}
	workspace := recordingCreateWorkspace{inner: inner, recorder: recorder}
	if err := workspace.Create(context.Background(), "/workspace/slot", "agent/create", "HEAD", false); err != nil {
		t.Fatalf("Create returned an error: %v", err)
	}
	if !inner.created || inner.removed {
		t.Fatalf("inner create/remove = created=%v removed=%v", inner.created, inner.removed)
	}
	if len(recorder.records) != 1 || recorder.records[0] != (capturedWorktreeRecord{path: "/workspace/slot", branch: "agent/create", source: state.WorktreeSourceCreate}) {
		t.Fatalf("records = %#v", recorder.records)
	}
}

func TestRecordingCreateWorkspaceForceRemovesOnRecorderFailure(t *testing.T) {
	inner := &trackingCreateWorkspaceStub{}
	recorder := &capturingWorktreeRecorder{recordErr: errors.New("record failed")}
	workspace := recordingCreateWorkspace{inner: inner, recorder: recorder}
	err := workspace.Create(context.Background(), "/workspace/slot", "agent/create", "HEAD", true)
	if err == nil || !strings.Contains(err.Error(), "record failed") {
		t.Fatalf("Create error = %v, want recording failure", err)
	}
	if !inner.removed || !inner.force {
		t.Fatalf("cleanup removed=%v force=%v, want forced remove", inner.removed, inner.force)
	}
}

func TestRecordingCreateWorkspaceJoinsRecordingAndCleanupFailures(t *testing.T) {
	inner := &trackingCreateWorkspaceStub{removeErr: errors.New("cleanup failed")}
	recorder := &capturingWorktreeRecorder{recordErr: errors.New("record failed")}
	workspace := recordingCreateWorkspace{inner: inner, recorder: recorder}
	err := workspace.Create(context.Background(), "/workspace/slot", "agent/create", "HEAD", false)
	if err == nil || !strings.Contains(err.Error(), "record failed") || !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("Create error = %v, want joined recording and cleanup failures", err)
	}
}

func TestRecordingCreateWorkspaceRemoveForwardsThenForgets(t *testing.T) {
	inner := &trackingCreateWorkspaceStub{}
	recorder := &capturingWorktreeRecorder{}
	workspace := recordingCreateWorkspace{inner: inner, recorder: recorder}
	if err := workspace.Remove(context.Background(), "/workspace/slot", true); err != nil {
		t.Fatalf("Remove returned an error: %v", err)
	}
	if !inner.removed || !inner.force {
		t.Fatalf("inner remove = removed=%v force=%v", inner.removed, inner.force)
	}
	if len(recorder.forgets) != 1 || recorder.forgets[0] != "/workspace/slot" {
		t.Fatalf("forgets = %#v", recorder.forgets)
	}
}

func TestRecordingCreateWorkspaceDoesNotForgetWhenInnerRemoveFails(t *testing.T) {
	inner := &trackingCreateWorkspaceStub{removeErr: errors.New("git remove failed")}
	recorder := &capturingWorktreeRecorder{}
	workspace := recordingCreateWorkspace{inner: inner, recorder: recorder}
	if err := workspace.Remove(context.Background(), "/workspace/slot", true); err == nil || !strings.Contains(err.Error(), "git remove failed") {
		t.Fatalf("Remove error = %v, want inner failure", err)
	}
	if len(recorder.forgets) != 0 {
		t.Fatalf("forgets = %#v, want none after inner failure", recorder.forgets)
	}
}

func TestRecordingAcquisitionWorktreeRecordsCreateAndAssignAndForwardsLock(t *testing.T) {
	inner := &runtimeWorkspaceStub{}
	recorder := &capturingWorktreeRecorder{}
	worktree := recordingAcquisitionWorktree{inner: inner, recorder: recorder}
	if err := worktree.Create(context.Background(), "/workspace/slot", "agent/acquire", "HEAD"); err != nil {
		t.Fatalf("Create returned an error: %v", err)
	}
	if err := worktree.Assign(context.Background(), "/workspace/slot", "agent/reused", "main"); err != nil {
		t.Fatalf("Assign returned an error: %v", err)
	}
	if err := worktree.Lock(context.Background(), "/workspace/slot"); err != nil {
		t.Fatalf("Lock returned an error: %v", err)
	}
	if len(inner.created) != 1 || len(inner.assigned) != 1 {
		t.Fatalf("inner created=%#v assigned=%#v", inner.created, inner.assigned)
	}
	if len(recorder.records) != 2 {
		t.Fatalf("records = %#v", recorder.records)
	}
	if recorder.records[0].source != state.WorktreeSourceAcquire || recorder.records[1].source != state.WorktreeSourceAcquire {
		t.Fatalf("sources = %#v", recorder.records)
	}
	if recorder.records[0].branch != "agent/acquire" || recorder.records[1].branch != "agent/reused" {
		t.Fatalf("branches = %#v", recorder.records)
	}
}

func TestRecordingAcquisitionWorktreeForwardsReturnWhenInnerImplementsIt(t *testing.T) {
	inner := &runtimeWorkspaceStub{}
	worktree := recordingAcquisitionWorktree{inner: inner, recorder: &capturingWorktreeRecorder{}}
	if err := worktree.Return(context.Background(), "/workspace/slot", true, []string{"node_modules"}); err != nil {
		t.Fatalf("Return returned an error: %v", err)
	}
	if len(inner.returned) != 1 || !strings.Contains(inner.returned[0], "/workspace/slot") {
		t.Fatalf("returned = %#v", inner.returned)
	}
}

func TestRecordingAcquisitionWorktreeFailsWhenInnerLacksReturn(t *testing.T) {
	worktree := recordingAcquisitionWorktree{inner: &acquisitionWorktreeCore{}, recorder: &capturingWorktreeRecorder{}}
	err := worktree.Return(context.Background(), "/workspace/slot", true, nil)
	if err == nil || err.Error() != "acquisition worktree cleanup is not configured" {
		t.Fatalf("Return error = %v, want unconfigured cleanup", err)
	}
}

func TestRecordingWarmWorkspaceRecordsCreateAndForwardsLock(t *testing.T) {
	inner := &runtimeDefaultsWarmWorktree{}
	recorder := &capturingWorktreeRecorder{}
	workspace := recordingWarmWorkspace{inner: inner, recorder: recorder}
	if err := workspace.Create(context.Background(), "/workspace/warm", "(warm)", "HEAD", true); err != nil {
		t.Fatalf("Create returned an error: %v", err)
	}
	if err := workspace.Lock(context.Background(), "/workspace/warm"); err != nil {
		t.Fatalf("Lock returned an error: %v", err)
	}
	if len(inner.created) != 1 || len(inner.locked) != 1 {
		t.Fatalf("inner created=%#v locked=%#v", inner.created, inner.locked)
	}
	if len(recorder.records) != 1 || recorder.records[0].source != state.WorktreeSourceWarm || recorder.records[0].path != "/workspace/warm" {
		t.Fatalf("records = %#v", recorder.records)
	}
}

func TestResolveWorktreeRecorderRejectsNilRecorderAndFactoryErrors(t *testing.T) {
	repository := git.Repository{Root: "/repo", CommonDir: "/repo/.git"}
	_, err := resolveWorktreeRecorder(context.Background(), func(context.Context, git.Repository) (WorktreeRecorder, error) {
		return nil, errors.New("factory failed")
	}, repository)
	if err == nil || !strings.Contains(err.Error(), "factory failed") {
		t.Fatalf("factory error = %v", err)
	}
	_, err = resolveWorktreeRecorder(context.Background(), func(context.Context, git.Repository) (WorktreeRecorder, error) {
		return nil, nil
	}, repository)
	if err == nil || err.Error() != "worktree recorder is not configured" {
		t.Fatalf("nil recorder error = %v", err)
	}
}

type capturingRepositoryIndexer struct {
	records []indexedRepository
	err     error
	called  int
}

type indexedRepository struct {
	commonDir, root string
}

func (indexer *capturingRepositoryIndexer) RecordRepository(_ context.Context, commonDir, root string) error {
	indexer.called++
	indexer.records = append(indexer.records, indexedRepository{commonDir: commonDir, root: root})
	return indexer.err
}

func TestIndexedWorktreeRecorderRecordsRepoThenIndex(t *testing.T) {
	repo := &capturingWorktreeRecorder{}
	index := &capturingRepositoryIndexer{}
	recorder := indexedWorktreeRecorder{repo: repo, index: index, commonDir: "/repo/.git", root: "/repo"}
	if err := recorder.RecordWorktree(context.Background(), "/workspace/slot", "agent/task", state.WorktreeSourceAcquire); err != nil {
		t.Fatalf("RecordWorktree returned an error: %v", err)
	}
	if len(repo.records) != 1 || repo.records[0].path != "/workspace/slot" {
		t.Fatalf("repo records = %#v", repo.records)
	}
	if len(index.records) != 1 || index.records[0] != (indexedRepository{commonDir: "/repo/.git", root: "/repo"}) {
		t.Fatalf("index records = %#v", index.records)
	}
}

func TestIndexedWorktreeRecorderRepoFailureShortCircuitsIndex(t *testing.T) {
	repo := &capturingWorktreeRecorder{recordErr: errors.New("registry write failed")}
	index := &capturingRepositoryIndexer{}
	recorder := indexedWorktreeRecorder{repo: repo, index: index, commonDir: "/repo/.git", root: "/repo"}
	err := recorder.RecordWorktree(context.Background(), "/workspace/slot", "agent/task", state.WorktreeSourceCreate)
	if err == nil || !strings.Contains(err.Error(), "registry write failed") {
		t.Fatalf("RecordWorktree error = %v", err)
	}
	if index.called != 0 {
		t.Fatalf("index was called after repo failure: %#v", index.records)
	}
}

func TestIndexedWorktreeRecorderIndexFailureSurfacesAfterRepoSuccess(t *testing.T) {
	repo := &capturingWorktreeRecorder{}
	index := &capturingRepositoryIndexer{err: errors.New("index write failed")}
	recorder := indexedWorktreeRecorder{repo: repo, index: index, commonDir: "/repo/.git", root: "/repo"}
	err := recorder.RecordWorktree(context.Background(), "/workspace/slot", "agent/task", state.WorktreeSourceWarm)
	if err == nil || !strings.Contains(err.Error(), "index write failed") {
		t.Fatalf("RecordWorktree error = %v", err)
	}
	if len(repo.records) != 1 {
		t.Fatalf("repo was not recorded before index failure: %#v", repo.records)
	}
}

func TestIndexedWorktreeRecorderForgetDoesNotTouchIndex(t *testing.T) {
	repo := &capturingWorktreeRecorder{}
	index := &capturingRepositoryIndexer{}
	recorder := indexedWorktreeRecorder{repo: repo, index: index, commonDir: "/repo/.git", root: "/repo"}
	if err := recorder.ForgetWorktree(context.Background(), "/workspace/slot"); err != nil {
		t.Fatalf("ForgetWorktree returned an error: %v", err)
	}
	if len(repo.forgets) != 1 || repo.forgets[0] != "/workspace/slot" {
		t.Fatalf("forgets = %#v", repo.forgets)
	}
	if index.called != 0 {
		t.Fatalf("ForgetWorktree touched the index: %#v", index.records)
	}
}
