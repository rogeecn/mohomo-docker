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
	fixture.failNextReload.Store(true)

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

type lifecycleFixture struct {
	config               CandidateConfig
	controllerURL        string
	startedPath          string
	lock                 sync.RWMutex
	response             string
	status               int
	subscriptionRequests atomic.Int32
	reloadRequests       atomic.Int32
	failNextReload       atomic.Bool
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	fixture := &lifecycleFixture{response: fullSubscription("first-node"), status: http.StatusOK}
	subscription := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fixture.subscriptionRequests.Add(1)
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
			if fixture.failNextReload.CompareAndSwap(true, false) {
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
printf started > %q
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
