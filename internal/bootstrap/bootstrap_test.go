package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
