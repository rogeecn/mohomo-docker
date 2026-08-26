package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestRunColdStartAndSignalUpdate(t *testing.T) {
	fixture := newLifecycleFixture(t)
	trigger := make(chan os.Signal, 1)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- Run(ctx, fixture.lifecycle(trigger)) }()

	fixture.waitStarted(t)
	waitFor(t, func() bool { return fixture.subscriptionRequests.Load() == 1 })
	assertContains(t, filepath.Join(fixture.config.DataDir, "last-good", "subscription.yaml"), "first-node")

	fixture.setSubscription("second-node", http.StatusOK)
	trigger <- syscall.SIGHUP
	waitFor(t, func() bool { return fixture.subscriptionRequests.Load() == 2 && fixture.reloadRequests.Load() == 1 })
	assertContains(t, filepath.Join(fixture.config.DataDir, "last-good", "subscription.yaml"), "second-node")

	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", err)
	}
}

func TestRunWarmCacheSurvivesImmediateUpdateFailureAndRestart(t *testing.T) {
	fixture := newLifecycleFixture(t)
	if err := PublishCandidate(context.Background(), fixture.config); err != nil {
		t.Fatal(err)
	}
	fixture.setSubscription("ignored", http.StatusServiceUnavailable)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- Run(ctx, fixture.lifecycle(nil)) }()
	fixture.waitStarted(t)
	waitFor(t, func() bool { return fixture.subscriptionRequests.Load() >= 2 })
	assertContains(t, filepath.Join(fixture.config.DataDir, "last-good", "subscription.yaml"), "first-node")
	select {
	case err := <-result:
		t.Fatalf("Run() stopped after warm-cache update failure: %v", err)
	default:
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", err)
	}
}

func TestRunColdFailureDoesNotStartMihomo(t *testing.T) {
	fixture := newLifecycleFixture(t)
	fixture.setSubscription("ignored", http.StatusBadGateway)
	err := Run(context.Background(), fixture.lifecycle(nil))
	if err == nil || !strings.Contains(err.Error(), "cold-start candidate failed") {
		t.Fatalf("Run() error = %v, want cold-start failure", err)
	}
	if _, statErr := os.Stat(fixture.startedPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Mihomo started without a valid cache: %v", statErr)
	}
}

func TestRunReloadFailureRestoresLastGood(t *testing.T) {
	fixture := newLifecycleFixture(t)
	if err := PublishCandidate(context.Background(), fixture.config); err != nil {
		t.Fatal(err)
	}
	wantTarget := readLastGood(t, fixture.config.DataDir)
	fixture.setSubscription("rejected-node", http.StatusOK)
	fixture.reloadFailures.Store(1)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- Run(ctx, fixture.lifecycle(nil)) }()
	fixture.waitStarted(t)
	waitFor(t, func() bool { return fixture.reloadRequests.Load() == 2 })
	if got := readLastGood(t, fixture.config.DataDir); got != wantTarget {
		t.Fatalf("last-good = %q, want restored %q", got, wantTarget)
	}
	assertContains(t, filepath.Join(fixture.config.DataDir, "last-good", "subscription.yaml"), "first-node")
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", err)
	}
}

func TestRunAndCandidateSerializeDataDirUpdates(t *testing.T) {
	fixture := newLifecycleFixture(t)
	if err := PublishCandidate(context.Background(), fixture.config); err != nil {
		t.Fatal(err)
	}
	fixture.setSubscription("second-node", http.StatusOK)
	fixture.blockNextSubscription.Store(true)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- Run(ctx, fixture.lifecycle(nil)) }()
	fixture.waitStarted(t)
	select {
	case <-fixture.subscriptionEntered:
	case <-time.After(5 * time.Second):
		close(fixture.subscriptionRelease)
		t.Fatal("Run did not enter the locked candidate update")
	}

	candidateResult := make(chan error, 1)
	go func() { candidateResult <- PublishCandidate(context.Background(), fixture.config) }()
	select {
	case err := <-candidateResult:
		close(fixture.subscriptionRelease)
		t.Fatalf("concurrent candidate bypassed the data lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(fixture.subscriptionRelease)
	if err := <-candidateResult; err != nil {
		t.Fatalf("concurrent PublishCandidate() error = %v", err)
	}
	assertContains(t, filepath.Join(fixture.config.DataDir, "last-good", "subscription.yaml"), "second-node")

	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", err)
	}
}

func TestRunWarmStartKeepsValidatedGenerationLocked(t *testing.T) {
	fixture := newLifecycleFixture(t)
	if err := PublishCandidate(context.Background(), fixture.config); err != nil {
		t.Fatal(err)
	}
	firstTarget := readLastGood(t, fixture.config.DataDir)
	fixture.setSubscription("second-node", http.StatusOK)
	validated := make(chan struct{})
	resume := make(chan struct{})
	lifecycle := fixture.lifecycle(nil)
	lifecycle.afterLastGoodValidated = func() {
		close(validated)
		<-resume
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- Run(ctx, lifecycle) }()
	select {
	case <-validated:
	case <-time.After(5 * time.Second):
		close(resume)
		t.Fatal("Run did not pause after warm generation validation")
	}

	candidateStarted := make(chan struct{})
	candidateResult := make(chan error, 1)
	go func() {
		close(candidateStarted)
		candidateResult <- PublishCandidate(context.Background(), fixture.config)
	}()
	<-candidateStarted
	select {
	case err := <-candidateResult:
		close(resume)
		t.Fatalf("candidate bypassed the warm-start data lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if got := fixture.subscriptionRequests.Load(); got != 1 {
		close(resume)
		t.Fatalf("subscription requests = %d before warm start resumed, want 1", got)
	}
	if _, err := os.Stat(fixture.startedPath); !errors.Is(err, os.ErrNotExist) {
		close(resume)
		t.Fatalf("Mihomo started before warm validation resumed: %v", err)
	}
	close(resume)
	fixture.waitStarted(t)
	if err := <-candidateResult; err != nil {
		t.Fatalf("concurrent PublishCandidate() error = %v", err)
	}

	activeTarget := "generations/a"
	if firstTarget == activeTarget {
		activeTarget = "generations/b"
	}
	if _, err := os.Stat(filepath.Join(fixture.config.DataDir, filepath.FromSlash(activeTarget))); err != nil {
		t.Fatalf("active generation %q was removed: %v", activeTarget, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.config.DataDir, "last-good")); err != nil {
		t.Fatalf("last-good is dangling: %v", err)
	}
	select {
	case err := <-result:
		t.Fatalf("Run() stopped after concurrent candidate: %v", err)
	default:
	}

	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", err)
	}
}

func TestRunRollbackPersistenceFailureStopsMihomo(t *testing.T) {
	fixture := newLifecycleFixture(t)
	if err := PublishCandidate(context.Background(), fixture.config); err != nil {
		t.Fatal(err)
	}
	wantTarget := readLastGood(t, fixture.config.DataDir)
	fixture.setSubscription("rejected-node", http.StatusOK)
	fixture.reloadFailures.Store(1)
	var syncCalls atomic.Int32
	fixture.config.directorySync = func(path string) error {
		if syncCalls.Add(1) == 3 {
			return errors.New("injected rollback sync failure")
		}
		return syncDirectory(path)
	}

	result := make(chan error, 1)
	go func() { result <- Run(context.Background(), fixture.lifecycle(nil)) }()
	fixture.waitStarted(t)
	err := waitResult(t, result)
	if !strings.Contains(err.Error(), "persist restored last-good pointer") {
		t.Fatalf("Run() error = %v, want rollback persistence failure", err)
	}
	if got := readLastGood(t, fixture.config.DataDir); got != wantTarget {
		t.Fatalf("last-good = %q, want restored %q", got, wantTarget)
	}
	fixture.waitStopped(t)
}

func TestRunSecondReloadFailureStopsMihomo(t *testing.T) {
	fixture := newLifecycleFixture(t)
	if err := PublishCandidate(context.Background(), fixture.config); err != nil {
		t.Fatal(err)
	}
	wantTarget := readLastGood(t, fixture.config.DataDir)
	fixture.setSubscription("rejected-node", http.StatusOK)
	fixture.reloadFailures.Store(2)

	result := make(chan error, 1)
	go func() { result <- Run(context.Background(), fixture.lifecycle(nil)) }()
	fixture.waitStarted(t)
	err := waitResult(t, result)
	if !strings.Contains(err.Error(), "reload restored last-good") {
		t.Fatalf("Run() error = %v, want second reload failure", err)
	}
	if got := readLastGood(t, fixture.config.DataDir); got != wantTarget {
		t.Fatalf("last-good = %q, want restored %q", got, wantTarget)
	}
	fixture.waitStopped(t)
}

type lifecycleFixture struct {
	config                CandidateConfig
	controllerURL         string
	startedPath           string
	lock                  sync.RWMutex
	response              string
	status                int
	subscriptionRequests  atomic.Int32
	reloadRequests        atomic.Int32
	reloadFailures        atomic.Int32
	blockNextSubscription atomic.Bool
	subscriptionEntered   chan struct{}
	subscriptionRelease   chan struct{}
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	fixture := &lifecycleFixture{
		response:            fullSubscription("first-node"),
		status:              http.StatusOK,
		subscriptionEntered: make(chan struct{}),
		subscriptionRelease: make(chan struct{}),
	}
	subscription := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fixture.subscriptionRequests.Add(1)
		if fixture.blockNextSubscription.CompareAndSwap(true, false) {
			close(fixture.subscriptionEntered)
			<-fixture.subscriptionRelease
		}
		fixture.lock.RLock()
		defer fixture.lock.RUnlock()
		writer.WriteHeader(fixture.status)
		_, _ = writer.Write([]byte(fixture.response))
	}))
	t.Cleanup(subscription.Close)
	controller := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/version":
			writer.WriteHeader(http.StatusOK)
		case request.Method == http.MethodPut && request.URL.Path == "/configs":
			fixture.reloadRequests.Add(1)
			if fixture.consumeReloadFailure() {
				http.Error(writer, "rejected", http.StatusInternalServerError)
				return
			}
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(controller.Close)

	directory := t.TempDir()
	fixture.startedPath = filepath.Join(directory, "started")
	secret := writeFixture(t, directory, "subscription-secret", subscription.URL+"\n")
	template := writeFixture(t, directory, "config.yaml", "mixed-port: 7890\nexternal-controller: 127.0.0.1:9090\nproxy-providers:\n  subscription:\n    type: file\n    path: ./subscription.yaml\n")
	mihomo := writeFixture(t, directory, "mihomo", fmt.Sprintf(`#!/bin/sh
set -eu
if [ "${1:-}" = -t ]; then
  directory=
  config=
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -d) directory=$2; shift 2 ;;
      -f) config=$2; shift 2 ;;
      *) shift ;;
    esac
  done
  test -s "$config"
  test -s "$directory/subscription.yaml"
  ! grep -F reject-validation "$directory/subscription.yaml" >/dev/null
  exit 0
fi
printf '%%s' $$ > %q
trap 'exit 0' TERM INT
while :; do sleep 1; done
`, fixture.startedPath))
	if err := os.Chmod(mihomo, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture.config = CandidateConfig{
		SecretPath:   secret,
		DataDir:      filepath.Join(directory, "data"),
		TemplatePath: template,
		MihomoBinary: mihomo,
	}
	fixture.controllerURL = controller.URL
	return fixture
}

func (fixture *lifecycleFixture) lifecycle(trigger <-chan os.Signal) LifecycleConfig {
	return LifecycleConfig{
		Candidate:      fixture.config,
		ControllerURL:  fixture.controllerURL,
		UpdateInterval: time.Hour,
		Trigger:        trigger,
	}
}

func (fixture *lifecycleFixture) setSubscription(name string, status int) {
	fixture.lock.Lock()
	defer fixture.lock.Unlock()
	fixture.response = fullSubscription(name)
	fixture.status = status
}

func (fixture *lifecycleFixture) waitStarted(t *testing.T) {
	t.Helper()
	waitFor(t, func() bool {
		_, err := os.Stat(fixture.startedPath)
		return err == nil
	})
}

func (fixture *lifecycleFixture) waitStopped(t *testing.T) {
	t.Helper()
	waitFor(t, func() bool {
		pidBytes, err := os.ReadFile(fixture.startedPath)
		if err != nil {
			return false
		}
		var pid int
		if _, err := fmt.Sscanf(string(pidBytes), "%d", &pid); err != nil {
			return false
		}
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	})
}

func (fixture *lifecycleFixture) consumeReloadFailure() bool {
	for {
		remaining := fixture.reloadFailures.Load()
		if remaining == 0 {
			return false
		}
		if fixture.reloadFailures.CompareAndSwap(remaining, remaining-1) {
			return true
		}
	}
}

func waitResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("Run() error = nil")
		}
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not fail closed")
		return nil
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
