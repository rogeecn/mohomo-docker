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
	"sync/atomic"
	"testing"
	"time"
)

func TestPrepareInitializesServerRuntime(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	root := filepath.Join(tempDir, "data")
	coreSource := writeFixture(t, tempDir, "mihomo", "mihomo-binary")
	configSource := writeFixture(t, tempDir, "config.yaml", "mixed-port: 7890\n")

	result, err := Prepare(Config{
		Root:         root,
		CoreSource:   coreSource,
		ConfigSource: configSource,
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if !result.CoreInitialized || !result.ConfigInitialized || !result.ServerSettingsChanged {
		t.Errorf("Prepare() result = %+v, want all initialization flags", result)
	}

	for _, directory := range []string{
		"bin", ".ssclash", "configs", "local-rules", "rule-providers",
		"proxy-providers", "subscriptions", "ui",
	} {
		info, statErr := os.Stat(filepath.Join(root, directory))
		if statErr != nil {
			t.Errorf("directory %q not created: %v", directory, statErr)
			continue
		}
		if !info.IsDir() {
			t.Errorf("path %q is not a directory", directory)
		}
	}

	assertFileContent(t, filepath.Join(root, "bin", "clash"), "mihomo-binary")
	assertFileContent(t, filepath.Join(root, "config.yaml"), "mixed-port: 7890\n")
	assertFileContent(t, filepath.Join(root, ".ssclash", "settings"), "OPERATING_MODE=server\nPROXY_MODE=none\n")

	coreInfo, err := os.Stat(filepath.Join(root, "bin", "clash"))
	if err != nil {
		t.Fatal(err)
	}
	if coreInfo.Mode().Perm() != 0o755 {
		t.Errorf("core mode = %o, want 755", coreInfo.Mode().Perm())
	}
}

func TestPreparePreservesUserDataAndForcesServerMode(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	root := filepath.Join(tempDir, "data")
	if err := os.MkdirAll(filepath.Join(root, ".ssclash"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, "bin"), "clash", "user-managed-core")
	writeFixture(t, root, "config.yaml", "user: config\n")
	writeFixture(t, filepath.Join(root, ".ssclash"), "settings", "LOG_LEVEL=debug\nOPERATING_MODE=gateway\nPROXY_MODE=tproxy\n")

	result, err := Prepare(Config{
		Root:         root,
		CoreSource:   writeFixture(t, tempDir, "mihomo", "image-core"),
		ConfigSource: writeFixture(t, tempDir, "default.yaml", "image: config\n"),
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if result.CoreInitialized || result.ConfigInitialized || !result.ServerSettingsChanged {
		t.Errorf("Prepare() result = %+v, want only server mode changed", result)
	}

	assertFileContent(t, filepath.Join(root, "bin", "clash"), "user-managed-core")
	assertFileContent(t, filepath.Join(root, "config.yaml"), "user: config\n")
	assertFileContent(t, filepath.Join(root, ".ssclash", "settings"), "LOG_LEVEL=debug\nOPERATING_MODE=server\nPROXY_MODE=none\n")
}

func TestEnsureAdminPasswordFailsClosedOnFreshVolume(t *testing.T) {
	t.Parallel()

	_, err := EnsureAdminPassword(t.TempDir(), "unused", "")
	if err == nil || !strings.Contains(err.Error(), "SSCLASH_PASSWORD") {
		t.Fatalf("EnsureAdminPassword() error = %v, want missing password error", err)
	}
}

func TestEnsureAdminPasswordInitializesOnlyWhenMissing(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(filepath.Join(root, ".ssclash"), 0o755); err != nil {
		t.Fatal(err)
	}
	binary := writeFixture(t, filepath.Join(root, "bin"), "ssclash", `#!/bin/sh
set -eu
[ "$1" = setpass ]
[ "$2" = fresh-volume-password ]
password="$(dirname "$0")/../.ssclash/password"
printf 'pbkdf2$test\n' > "$password"
chmod 0600 "$password"
`)
	if err := os.Chmod(binary, 0o755); err != nil {
		t.Fatal(err)
	}

	initialized, err := EnsureAdminPassword(root, binary, "fresh-volume-password")
	if err != nil {
		t.Fatalf("EnsureAdminPassword() error = %v", err)
	}
	if !initialized {
		t.Fatal("EnsureAdminPassword() initialized = false, want true")
	}
	assertFileContent(t, filepath.Join(root, ".ssclash", "password"), "pbkdf2$test\n")

	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	initialized, err = EnsureAdminPassword(root, binary, "replacement-password")
	if err != nil {
		t.Fatalf("EnsureAdminPassword() existing password error = %v", err)
	}
	if initialized {
		t.Fatal("EnsureAdminPassword() replaced existing password")
	}
	assertFileContent(t, filepath.Join(root, ".ssclash", "password"), "pbkdf2$test\n")
}

func TestChildEnvironmentRemovesCredentials(t *testing.T) {
	t.Setenv("SUBSCRIPTION_URL", "https://subscription.example.invalid/?token=secret")
	t.Setenv("SSCLASH_PASSWORD", "secret-password")

	environment := strings.Join(childEnvironment(), "\n")
	for _, key := range []string{"SUBSCRIPTION_URL=", "SSCLASH_PASSWORD="} {
		if strings.Contains(environment, key) {
			t.Errorf("childEnvironment() retained %s", key)
		}
	}
}

func TestPrepareRejectsUnsafeOrAmbiguousState(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	coreSource := writeFixture(t, tempDir, "mihomo", "core")
	configSource := writeFixture(t, tempDir, "config.yaml", "config")

	tests := []struct {
		name    string
		config  Config
		setup   func(t *testing.T, root string)
		wantErr string
	}{
		{
			name: "filesystem root",
			config: Config{
				Root:         "/",
				CoreSource:   coreSource,
				ConfigSource: configSource,
			},
			wantErr: "unsafe root",
		},
		{
			name: "missing core source",
			config: Config{
				Root:         filepath.Join(tempDir, "missing-core"),
				CoreSource:   filepath.Join(tempDir, "does-not-exist"),
				ConfigSource: configSource,
			},
			wantErr: "core source",
		},
		{
			name: "duplicate operating mode",
			config: Config{
				Root:         filepath.Join(tempDir, "duplicate-mode"),
				CoreSource:   coreSource,
				ConfigSource: configSource,
			},
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(root, ".ssclash"), 0o755); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, filepath.Join(root, ".ssclash"), "settings", "OPERATING_MODE=gateway\nOPERATING_MODE=server\n")
			},
			wantErr: "multiple OPERATING_MODE",
		},
		{
			name: "duplicate proxy mode",
			config: Config{
				Root:         filepath.Join(tempDir, "duplicate-proxy-mode"),
				CoreSource:   coreSource,
				ConfigSource: configSource,
			},
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(root, ".ssclash"), 0o755); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, filepath.Join(root, ".ssclash"), "settings", "PROXY_MODE=tproxy\nPROXY_MODE=none\n")
			},
			wantErr: "multiple PROXY_MODE",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.setup != nil {
				testCase.setup(t, testCase.config.Root)
			}
			_, err := Prepare(testCase.config)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("Prepare() error = %v, want substring %q", err, testCase.wantErr)
			}
		})
	}
}

func TestUpdateSubscriptionKeepsPreviousValidFile(t *testing.T) {
	t.Parallel()

	response := "proxies:\n  - name: valid\n"
	var responseLock sync.RWMutex
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		responseLock.RLock()
		defer responseLock.RUnlock()
		_, _ = writer.Write([]byte(response))
	}))
	defer server.Close()

	target := filepath.Join(t.TempDir(), "subscription.yaml")
	validate := func(path string) error {
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(content), "invalid") {
			return errors.New("invalid provider")
		}
		return nil
	}
	if err := updateSubscription(context.Background(), server.Client(), server.URL, target, validate); err != nil {
		t.Fatalf("initial updateSubscription() error = %v", err)
	}
	responseLock.Lock()
	response = "invalid"
	responseLock.Unlock()
	if err := updateSubscription(context.Background(), server.Client(), server.URL, target, validate); err == nil {
		t.Fatal("updateSubscription() accepted invalid replacement")
	}
	assertFileContent(t, target, "proxies:\n  - name: valid\n")
}

func TestSubscriptionErrorsDoNotExposeURL(t *testing.T) {
	t.Parallel()

	secretURL := "https://subscription.example.invalid/feed?token=do-not-log"
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New(request.URL.String())
	})}
	err := updateSubscription(context.Background(), client, secretURL, filepath.Join(t.TempDir(), "subscription.yaml"), func(string) error { return nil })
	if err == nil {
		t.Fatal("updateSubscription() error = nil")
	}
	if strings.Contains(err.Error(), "do-not-log") || strings.Contains(err.Error(), secretURL) {
		t.Fatalf("updateSubscription() leaked subscription URL: %v", err)
	}
}

func TestUpdateAndReloadRestoresRuntimeAfterAmbiguousFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("proxies:\n  - name: updated\n"))
	}))
	defer server.Close()

	for _, testCase := range []struct {
		name      string
		recover   bool
		wantFatal bool
	}{
		{name: "rollback reload succeeds", recover: true},
		{name: "rollback reload fails", wantFatal: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			target := writeFixture(t, t.TempDir(), "subscription.yaml", "proxies:\n  - name: previous\n")
			var applied []string
			reload := func(context.Context) error {
				content, err := os.ReadFile(target)
				if err != nil {
					return err
				}
				applied = append(applied, string(content))
				if len(applied) == 1 || !testCase.recover {
					return errors.New("connection lost after server applied provider")
				}
				return nil
			}

			err := updateAndReload(context.Background(), server.Client(), RuntimeConfig{SubscriptionURL: server.URL}, target, func(string) error { return nil }, reload)
			if err == nil {
				t.Fatal("updateAndReload() error = nil")
			}
			if got := errors.Is(err, errMihomoStateUncertain); got != testCase.wantFatal {
				t.Fatalf("errors.Is(state uncertain) = %t, want %t: %v", got, testCase.wantFatal, err)
			}
			if len(applied) != 2 || !strings.Contains(applied[0], "updated") || !strings.Contains(applied[1], "previous") {
				t.Fatalf("reload sequence = %q, want updated then previous", applied)
			}
			assertFileContent(t, target, "proxies:\n  - name: previous\n")
		})
	}
}

func TestRunStopsServicesWhenRollbackReloadFails(t *testing.T) {
	tempDir := t.TempDir()
	binary := writeFixture(t, tempDir, "fake-service", "#!/bin/sh\nif [ \"$1\" = -t ]; then exit 0; fi\nexec sleep 3600\n")
	if err := os.Chmod(binary, 0o755); err != nil {
		t.Fatal(err)
	}
	config := writeFixture(t, tempDir, "config.yaml", "proxy-providers:\n  subscription:\n    type: file\n    path: ./subscription.yaml\n")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		name := "updated"
		if requests.Add(1) == 1 {
			name = "initial"
		}
		_, _ = writer.Write([]byte("proxies:\n  - name: " + name + "\n"))
	}))
	defer server.Close()

	runResult := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		runResult <- Run(ctx, RuntimeConfig{
			CoreBinary:      binary,
			SSClashBinary:   binary,
			ConfigSource:    config,
			RuntimeDir:      filepath.Join(tempDir, "runtime"),
			SubscriptionURL: server.URL,
			UpdateInterval:  20 * time.Millisecond,
		})
	}()
	var err error
	select {
	case err = <-runResult:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not reach rollback failure")
	}
	if !errors.Is(err, errMihomoStateUncertain) {
		t.Fatalf("Run() error = %v, want uncertain Mihomo state", err)
	}
	if requests.Load() < 2 {
		t.Fatalf("subscription requests = %d, want initial fetch and timed update", requests.Load())
	}
	assertFileContent(t, filepath.Join(tempDir, "runtime", "subscription.yaml"), "proxies:\n  - name: initial\n")
}

func TestValidateSubscriptionURL(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"", "relative/path", "ftp://example.com/feed", "https://user@example.com/feed"} {
		if err := validateSubscriptionURL(raw); err == nil {
			t.Errorf("validateSubscriptionURL(%q) error = nil", raw)
		}
	}
	if err := validateSubscriptionURL("https://example.com/feed"); err != nil {
		t.Fatalf("validateSubscriptionURL() error = %v", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func writeFixture(t *testing.T, directory, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(content) != want {
		t.Errorf("content of %s = %q, want %q", path, content, want)
	}
}
