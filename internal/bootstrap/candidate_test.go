package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPublishCandidateGeneratesValidatedLastGood(t *testing.T) {
	t.Parallel()

	var lock sync.RWMutex
	response := fullSubscription("first-node")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		lock.RLock()
		defer lock.RUnlock()
		_, _ = writer.Write([]byte(response))
	}))
	defer server.Close()

	config := candidateFixture(t, server.URL+"?token=FAKE-SECRET")
	if err := PublishCandidate(context.Background(), config); err != nil {
		t.Fatalf("PublishCandidate() error = %v", err)
	}
	firstTarget := readLastGood(t, config.DataDir)
	if firstTarget != "generations/a" {
		t.Fatalf("last-good target = %q, want generations/a", firstTarget)
	}
	firstDir := filepath.Join(config.DataDir, filepath.FromSlash(firstTarget))
	assertContains(t, filepath.Join(firstDir, "config.yaml"), "external-controller: 0.0.0.0:9090")
	assertContains(t, filepath.Join(firstDir, "subscription.yaml"), "name: first-node")
	assertNotContains(t, filepath.Join(firstDir, "subscription.yaml"), "proxy-groups:")
	for _, name := range []string{"config.yaml", "subscription.yaml"} {
		info, err := os.Stat(filepath.Join(firstDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 600", name, info.Mode().Perm())
		}
	}

	lock.Lock()
	response = fullSubscription("second-node")
	lock.Unlock()
	if err := PublishCandidate(context.Background(), config); err != nil {
		t.Fatalf("second PublishCandidate() error = %v", err)
	}
	secondTarget := readLastGood(t, config.DataDir)
	if secondTarget != "generations/b" {
		t.Fatalf("last-good target = %q, want generations/b", secondTarget)
	}
	assertContains(t, filepath.Join(config.DataDir, filepath.FromSlash(secondTarget), "subscription.yaml"), "name: second-node")
	assertContains(t, filepath.Join(firstDir, "subscription.yaml"), "name: first-node")
}

func TestPublishCandidateFailureMatrixKeepsLastGoodAndRedactsInput(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		response  string
		status    int
		secret    string
		transport bool
	}{
		{name: "invalid secret URL", secret: "not-a-url-FAKE-SECRET"},
		{name: "request failure", transport: true},
		{name: "HTTP failure", status: http.StatusServiceUnavailable},
		{name: "empty response"},
		{name: "oversized response", response: strings.Repeat("x", maxSubscriptionSize+1)},
		{name: "invalid YAML", response: "proxies: ["},
		{name: "missing proxies", response: "proxy-groups: []\n"},
		{name: "Mihomo rejection", response: fullSubscription("reject-validation")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var lock sync.RWMutex
			response := fullSubscription("last-good-node")
			status := http.StatusOK
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				lock.RLock()
				defer lock.RUnlock()
				writer.WriteHeader(status)
				_, _ = writer.Write([]byte(response))
			}))
			defer server.Close()

			config := candidateFixture(t, server.URL+"?token=FAKE-SECRET")
			if err := PublishCandidate(context.Background(), config); err != nil {
				t.Fatalf("initial PublishCandidate() error = %v", err)
			}
			wantTarget := readLastGood(t, config.DataDir)
			wantSubscription, err := os.ReadFile(filepath.Join(config.DataDir, filepath.FromSlash(wantTarget), "subscription.yaml"))
			if err != nil {
				t.Fatal(err)
			}

			lock.Lock()
			response = testCase.response
			if testCase.status != 0 {
				status = testCase.status
			}
			lock.Unlock()
			if testCase.secret != "" {
				if err := os.WriteFile(config.SecretPath, []byte(testCase.secret), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if testCase.transport {
				config.Client = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("FAKE-SECRET transport detail")
				})}
			}

			err = PublishCandidate(context.Background(), config)
			if err == nil {
				t.Fatal("PublishCandidate() error = nil")
			}
			if strings.Contains(err.Error(), "FAKE-SECRET") || strings.Contains(err.Error(), "reject-validation") {
				t.Fatalf("PublishCandidate() leaked sensitive input: %v", err)
			}
			if got := readLastGood(t, config.DataDir); got != wantTarget {
				t.Fatalf("last-good target = %q, want unchanged %q", got, wantTarget)
			}
			gotSubscription, err := os.ReadFile(filepath.Join(config.DataDir, filepath.FromSlash(wantTarget), "subscription.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if string(gotSubscription) != string(wantSubscription) {
				t.Fatal("failed update changed last-good subscription")
			}
		})
	}
}

func TestPublishCandidateRejectsUnmanagedLastGood(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(fullSubscription("new-node")))
	}))
	defer server.Close()
	config := candidateFixture(t, server.URL)
	if err := os.MkdirAll(config.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, config.DataDir, "last-good", "operator-owned")

	err := PublishCandidate(context.Background(), config)
	if err == nil || !strings.Contains(err.Error(), "managed symlink") {
		t.Fatalf("PublishCandidate() error = %v, want unmanaged last-good rejection", err)
	}
	assertFileContent(t, filepath.Join(config.DataDir, "last-good"), "operator-owned")
}

func candidateFixture(t *testing.T, endpoint string) CandidateConfig {
	t.Helper()
	tempDir := t.TempDir()
	secret := writeFixture(t, tempDir, "subscription-secret", endpoint+"\n")
	mihomo := writeFixture(t, tempDir, "mihomo", `#!/bin/sh
set -eu
test "$1" = -t
directory=
config=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -d) directory=$2; shift 2 ;;
    -f) config=$2; shift 2 ;;
    *) shift ;;
  esac
done
test -n "$directory" -a -n "$config"
grep -F 'external-controller: 0.0.0.0:9090' "$config" >/dev/null
grep -F 'proxies:' "$directory/subscription.yaml" >/dev/null
! grep -F 'reject-validation' "$directory/subscription.yaml" >/dev/null
`)
	if err := os.Chmod(mihomo, 0o755); err != nil {
		t.Fatal(err)
	}
	return CandidateConfig{
		SecretPath:   secret,
		DataDir:      filepath.Join(tempDir, "data"),
		TemplatePath: filepath.Join("..", "..", "config", "config.yaml"),
		MihomoBinary: mihomo,
	}
}

func fullSubscription(name string) string {
	return "mixed-port: 1234\nproxies:\n  - name: " + name + "\n    type: socks5\n    server: 127.0.0.1\n    port: 9\nproxy-groups: []\n"
}

func readLastGood(t *testing.T, dataDir string) string {
	t.Helper()
	target, err := os.Readlink(filepath.Join(dataDir, "last-good"))
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func assertContains(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), want) {
		t.Errorf("%s does not contain %q", path, want)
	}
}

func assertNotContains(t *testing.T, path, unwanted string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), unwanted) {
		t.Errorf("%s contains %q", path, unwanted)
	}
}
