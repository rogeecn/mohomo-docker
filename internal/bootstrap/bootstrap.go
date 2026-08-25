package bootstrap

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	maxSubscriptionSize      = 16 << 20
	minAdminPasswordLength   = 12
	managedConfigVersion     = "1"
	managedConfigVersionFile = ".mohomo-docker-config-version"
	managedChinaIPProvider   = "  ChinaIp: {type: file, behavior: ipcidr, format: yaml, path: /usr/local/share/ssclash/rules/ChinaIp.yaml}\n"
	managedChinaIPRule       = "  - RULE-SET,ChinaIp,🎯 全球直连"
	legacyChinaIPRule        = "  - GEOIP,CN,🎯 全球直连"
	defaultControllerURL     = "http://127.0.0.1:9090"
)

var errMihomoStateUncertain = errors.New("Mihomo subscription state could not be restored")

type enforcedSetting struct {
	key   string
	value string
}

var serverSettings = []enforcedSetting{
	{key: "OPERATING_MODE=", value: "server"},
	{key: "PROXY_MODE=", value: "none"},
}

var runtimeDirectories = []string{
	"bin",
	".ssclash",
	"configs",
	"local-rules",
	"subscriptions",
	"ui",
}

var managedProviderDirectories = []string{"rule-providers", "proxy-providers"}

type Config struct {
	Root         string
	SSClashTemp  string
	CoreSource   string
	ConfigSource string
}

type Result struct {
	CoreInitialized       bool
	ConfigInitialized     bool
	ConfigMigrated        bool
	ServerSettingsChanged bool
}

type RuntimeConfig struct {
	Root            string
	CoreBinary      string
	SSClashBinary   string
	ConfigSource    string
	RuntimeDir      string
	SubscriptionURL string
	ControllerURL   string
	UpdateInterval  time.Duration
}

func Prepare(config Config) (Result, error) {
	var result Result
	root := filepath.Clean(config.Root)
	if root == "." || root == string(filepath.Separator) {
		return result, fmt.Errorf("unsafe root %q", config.Root)
	}
	if !filepath.IsAbs(root) {
		return result, fmt.Errorf("root must be absolute: %q", config.Root)
	}
	ssclashTemp := filepath.Clean(config.SSClashTemp)
	if !filepath.IsAbs(ssclashTemp) || ssclashTemp == string(filepath.Separator) {
		return result, fmt.Errorf("unsafe SSClash temporary directory %q", config.SSClashTemp)
	}
	if err := validateSource(config.CoreSource, "core source"); err != nil {
		return result, err
	}
	if err := validateSource(config.ConfigSource, "config source"); err != nil {
		return result, err
	}

	for _, directory := range runtimeDirectories {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			return result, fmt.Errorf("create runtime directory %s: %w", directory, err)
		}
	}
	for _, directory := range managedProviderDirectories {
		if err := reconcileManagedProviderDirectory(root, ssclashTemp, directory); err != nil {
			return result, err
		}
	}

	var err error
	result.CoreInitialized, err = copyIfAbsent(config.CoreSource, filepath.Join(root, "bin", "clash"), 0o755)
	if err != nil {
		return result, fmt.Errorf("initialize Mihomo core: %w", err)
	}
	result.ConfigInitialized, result.ConfigMigrated, err = prepareManagedConfig(
		config.ConfigSource,
		filepath.Join(root, "config.yaml"),
		filepath.Join(root, managedConfigVersionFile),
	)
	if err != nil {
		return result, fmt.Errorf("prepare config: %w", err)
	}
	result.ServerSettingsChanged, err = enforceServerSettings(filepath.Join(root, ".ssclash", "settings"))
	if err != nil {
		return result, fmt.Errorf("enforce server settings: %w", err)
	}

	return result, nil
}

func EnsureAdminPassword(root, binary, password string) (bool, error) {
	root = filepath.Clean(root)
	if root == "." || root == string(filepath.Separator) || !filepath.IsAbs(root) {
		return false, fmt.Errorf("unsafe root %q", root)
	}
	passwordPath := filepath.Join(root, ".ssclash", "password")
	configured, err := adminPasswordConfigured(passwordPath)
	if err != nil {
		return false, err
	}
	if configured {
		return false, nil
	}
	if password == "" {
		return false, errors.New("SSCLASH_PASSWORD is required to initialize a fresh volume")
	}
	if len(password) < minAdminPasswordLength {
		return false, fmt.Errorf("SSCLASH_PASSWORD must be at least %d characters", minAdminPasswordLength)
	}
	if err := validateSource(binary, "SSClash binary"); err != nil {
		return false, err
	}

	command := exec.Command(binary, "setpass", password)
	command.Env = childEnvironment()
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return false, errors.New("SSClash password initialization failed")
	}
	configured, err = adminPasswordConfigured(passwordPath)
	if err != nil {
		return false, err
	}
	if !configured {
		return false, errors.New("SSClash password initialization did not create an authentication file")
	}
	return true, nil
}

func adminPasswordConfigured(path string) (bool, error) {
	return adminPasswordConfiguredFor(path, uint32(os.Geteuid()), uint32(os.Getegid()))
}

func adminPasswordConfiguredFor(path string, expectedUID, expectedGID uint32) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect SSClash authentication file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("SSClash authentication file must be a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return false, fmt.Errorf("SSClash authentication file permissions are %o; want 600", info.Mode().Perm())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, errors.New("SSClash authentication file ownership could not be verified")
	}
	if stat.Uid != expectedUID || stat.Gid != expectedGID {
		return false, fmt.Errorf("SSClash authentication file owner is %d:%d; want %d:%d", stat.Uid, stat.Gid, expectedUID, expectedGID)
	}

	file, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("read SSClash authentication file: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect opened SSClash authentication file: %w", err)
	}
	if !os.SameFile(info, openedInfo) {
		return false, errors.New("SSClash authentication file changed while being verified")
	}
	content, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil {
		return false, fmt.Errorf("read SSClash authentication file: %w", err)
	}
	if len(content) > 256 {
		return false, errors.New("SSClash authentication file is too large")
	}
	if err := validateAdminPasswordHash(content); err != nil {
		return false, err
	}
	return true, nil
}

func validateAdminPasswordHash(content []byte) error {
	text := string(content)
	if !strings.HasSuffix(text, "\n") {
		return errors.New("SSClash authentication file has an invalid password hash")
	}
	parts := strings.Split(strings.TrimSuffix(text, "\n"), "$")
	if len(parts) != 4 || parts[0] != "pbkdf2" || parts[1] != "120000" || len(parts[2]) != 32 || len(parts[3]) != 64 {
		return errors.New("SSClash authentication file has an invalid password hash")
	}
	if _, err := hex.DecodeString(parts[2]); err != nil {
		return errors.New("SSClash authentication file has an invalid password hash")
	}
	if _, err := hex.DecodeString(parts[3]); err != nil {
		return errors.New("SSClash authentication file has an invalid password hash")
	}
	return nil
}

func reconcileManagedProviderDirectory(root, ssclashTemp, directory string) error {
	path := filepath.Join(root, directory)
	expectedTarget := filepath.Join(ssclashTemp, directory)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create runtime directory %s: %w", directory, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect runtime directory %s: %w", directory, err)
	}
	if info.IsDir() {
		return nil
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("runtime path %s is not a directory", directory)
	}
	target, err := os.Readlink(path)
	if err != nil {
		return fmt.Errorf("read runtime symlink %s: %w", directory, err)
	}
	if target != expectedTarget {
		return fmt.Errorf("runtime path %s has unexpected symlink target %q", directory, target)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove managed runtime symlink %s: %w", directory, err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("recreate runtime directory %s: %w", directory, err)
	}
	return nil
}

func Run(ctx context.Context, config RuntimeConfig) error {
	if err := validateSubscriptionURL(config.SubscriptionURL); err != nil {
		return err
	}
	if config.UpdateInterval <= 0 {
		return errors.New("subscription update interval must be positive")
	}
	root := filepath.Clean(config.Root)
	if !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return fmt.Errorf("unsafe root %q", config.Root)
	}
	runtimeDir := filepath.Clean(config.RuntimeDir)
	if !filepath.IsAbs(runtimeDir) || runtimeDir == string(filepath.Separator) {
		return fmt.Errorf("unsafe runtime directory %q", config.RuntimeDir)
	}
	for path, label := range map[string]string{
		config.CoreBinary:    "Mihomo core",
		config.SSClashBinary: "SSClash binary",
		config.ConfigSource:  "config source",
	} {
		if err := validateSource(path, label); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return fmt.Errorf("create in-memory runtime directory: %w", err)
	}

	runtimeConfig := filepath.Join(runtimeDir, "config.yaml")
	if err := copyFile(config.ConfigSource, runtimeConfig, 0o600); err != nil {
		return fmt.Errorf("prepare in-memory config: %w", err)
	}
	activeSubscription := filepath.Join(runtimeDir, "subscription.yaml")
	client := &http.Client{Timeout: 30 * time.Second}
	controllerURL := strings.TrimRight(config.ControllerURL, "/")
	if controllerURL == "" {
		controllerURL = defaultControllerURL
	}
	validate := func(candidate string) error {
		return validateSubscription(ctx, config, runtimeConfig, candidate)
	}
	reload := func(ctx context.Context) error {
		return reloadSubscription(ctx, client, controllerURL)
	}
	if err := updateSubscription(ctx, client, config.SubscriptionURL, activeSubscription, validate); err != nil {
		return fmt.Errorf("initial subscription update failed: %w", err)
	}
	if err := validateMihomoConfig(ctx, config.CoreBinary, runtimeDir, runtimeConfig); err != nil {
		return errors.New("generated Mihomo configuration failed validation")
	}
	if err := ensureSubscriptionLink(filepath.Join(root, "subscription.yaml"), activeSubscription); err != nil {
		return err
	}

	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ssclash := serviceCommand(serviceCtx, config.SSClashBinary, "serve")
	if err := ssclash.Start(); err != nil {
		return fmt.Errorf("start SSClash: %w", err)
	}
	log.Printf("bootstrap: SSClash started mode=server core_owner=ssclash subscription_update_interval=%s", config.UpdateInterval)

	exit := make(chan error, 1)
	go func() { exit <- ssclash.Wait() }()
	ticker := time.NewTicker(config.UpdateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			cancel()
			<-exit
			return ctx.Err()
		case err := <-exit:
			if err == nil {
				return errors.New("SSClash exited")
			}
			return fmt.Errorf("SSClash exited: %w", err)
		case <-ticker.C:
			running := mihomoRunning(ctx, client, controllerURL)
			var err error
			if running {
				err = updateAndReload(ctx, client, config, activeSubscription, validate, reload)
			} else {
				err = updateSubscription(ctx, client, config.SubscriptionURL, activeSubscription, validate)
			}
			if errors.Is(err, errMihomoStateUncertain) {
				log.Print("bootstrap: subscription rollback failed; stopping SSClash")
				cancel()
				<-exit
				return err
			} else if err != nil {
				log.Print("bootstrap: subscription update rejected; keeping previous valid configuration")
				continue
			}
			if running {
				log.Print("bootstrap: subscription updated and reloaded")
			} else {
				log.Print("bootstrap: subscription updated; Mihomo is stopped")
			}
		}
	}
}

func ensureSubscriptionLink(path, target string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Symlink(target, path); err != nil {
			return fmt.Errorf("create runtime subscription link: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect runtime subscription link: %w", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return errors.New("runtime subscription path is not a managed symlink")
	}
	existingTarget, err := os.Readlink(path)
	if err != nil {
		return fmt.Errorf("read runtime subscription link: %w", err)
	}
	if existingTarget != target {
		return fmt.Errorf("runtime subscription link has unexpected target %q", existingTarget)
	}
	return nil
}

func validateSubscriptionURL(raw string) error {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return errors.New("SUBSCRIPTION_URL must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil {
		return errors.New("SUBSCRIPTION_URL must not contain user information")
	}
	return nil
}

func updateSubscription(ctx context.Context, client *http.Client, endpoint, target string, validate func(string) error) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("create subscription request")
	}
	request.Header.Set("Accept", "application/yaml, text/yaml, text/plain")
	request.Header.Set("User-Agent", "mihomo")
	response, err := client.Do(request)
	if err != nil {
		return errors.New("subscription request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("subscription endpoint returned HTTP %d", response.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, maxSubscriptionSize+1))
	if err != nil {
		return errors.New("read subscription response")
	}
	if len(content) == 0 || len(content) > maxSubscriptionSize {
		return errors.New("subscription response is empty or too large")
	}

	candidate := filepath.Join(filepath.Dir(target), ".subscription-candidate.yaml")
	if err := atomicWrite(candidate, 0o600, func(output *os.File) error {
		_, err := output.Write(content)
		return err
	}); err != nil {
		return fmt.Errorf("write subscription candidate: %w", err)
	}
	defer os.Remove(candidate)
	if err := validate(candidate); err != nil {
		return errors.New("subscription candidate failed Mihomo validation")
	}
	if err := os.Rename(candidate, target); err != nil {
		return fmt.Errorf("activate subscription candidate: %w", err)
	}
	return nil
}

func validateSubscription(ctx context.Context, config RuntimeConfig, runtimeConfig, candidate string) error {
	content, err := os.ReadFile(runtimeConfig)
	if err != nil {
		return err
	}
	candidateConfig := strings.Replace(string(content), "path: ./subscription.yaml", "path: ./"+filepath.Base(candidate), 1)
	if candidateConfig == string(content) {
		return errors.New("subscription provider path is missing from config")
	}
	path := filepath.Join(config.RuntimeDir, ".candidate-config.yaml")
	if err := atomicWrite(path, 0o600, func(output *os.File) error {
		_, err := output.WriteString(candidateConfig)
		return err
	}); err != nil {
		return err
	}
	defer os.Remove(path)
	return validateMihomoConfig(ctx, config.CoreBinary, config.RuntimeDir, path)
}

func validateMihomoConfig(ctx context.Context, binary, runtimeDir, configPath string) error {
	command := exec.CommandContext(ctx, binary, "-t", "-d", runtimeDir, "-f", configPath)
	command.Env = childEnvironment()
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return errors.New("Mihomo validation failed")
	}
	return nil
}

func updateAndReload(ctx context.Context, client *http.Client, config RuntimeConfig, target string, validate func(string) error, reload func(context.Context) error) error {
	previous, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	if err := updateSubscription(ctx, client, config.SubscriptionURL, target, validate); err != nil {
		return err
	}
	if err := reload(ctx); err == nil {
		return nil
	}
	if rollbackErr := atomicWrite(target, 0o600, func(output *os.File) error {
		_, writeErr := output.Write(previous)
		return writeErr
	}); rollbackErr != nil {
		return fmt.Errorf("%w: restore previous subscription file: %v", errMihomoStateUncertain, rollbackErr)
	}
	if err := reload(ctx); err != nil {
		return fmt.Errorf("%w: reload previous subscription", errMihomoStateUncertain)
	}
	return errors.New("new subscription reload failed; previous subscription restored")
}

func reloadSubscription(ctx context.Context, client *http.Client, controllerURL string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, controllerURL+"/providers/proxies/subscription", nil)
	if err != nil {
		return errors.New("create Mihomo reload request")
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("Mihomo reload request failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Mihomo reload returned HTTP %d", response.StatusCode)
	}
	return nil
}

func mihomoRunning(ctx context.Context, client *http.Client, controllerURL string) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, controllerURL+"/version", nil)
	if err != nil {
		return false
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return true
}

func serviceCommand(ctx context.Context, binary string, arguments ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Env = childEnvironment()
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Cancel = func() error {
		return command.Process.Signal(syscall.SIGTERM)
	}
	command.WaitDelay = 10 * time.Second
	return command
}

func childEnvironment() []string {
	environment := os.Environ()
	result := environment[:0]
	for _, entry := range environment {
		if strings.HasPrefix(entry, "SUBSCRIPTION_URL=") || strings.HasPrefix(entry, "SSCLASH_PASSWORD=") {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func copyFile(source, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	return atomicWrite(target, mode, func(output *os.File) error {
		_, err := io.Copy(output, input)
		return err
	})
}

func validateSource(path, label string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s %q: %w", label, path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s %q is not a regular file", label, path)
	}
	if info.Size() == 0 {
		return fmt.Errorf("%s %q is empty", label, path)
	}
	return nil
}

func copyIfAbsent(source, target string, mode os.FileMode) (bool, error) {
	info, err := os.Stat(target)
	if err == nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("target %q is not a regular file", target)
		}
		if info.Size() == 0 {
			return false, fmt.Errorf("target %q is empty", target)
		}
		return false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect target %q: %w", target, err)
	}

	input, err := os.Open(source)
	if err != nil {
		return false, fmt.Errorf("open source %q: %w", source, err)
	}
	defer input.Close()

	err = atomicWrite(target, mode, func(output *os.File) error {
		if _, copyErr := io.Copy(output, input); copyErr != nil {
			return fmt.Errorf("copy %q to %q: %w", source, target, copyErr)
		}
		return nil
	})
	return err == nil, err
}

func prepareManagedConfig(source, target, versionPath string) (bool, bool, error) {
	if err := validateManagedConfigVersion(versionPath); err != nil {
		return false, false, err
	}
	current, err := os.ReadFile(source)
	if err != nil {
		return false, false, fmt.Errorf("read managed config source: %w", err)
	}

	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		if err := writeManagedConfig(target, current); err != nil {
			return false, false, err
		}
		if err := writeManagedConfigVersion(versionPath); err != nil {
			return false, false, err
		}
		return true, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("inspect config %q: %w", target, err)
	}
	if !info.Mode().IsRegular() {
		return false, false, fmt.Errorf("config %q is not a regular file", target)
	}
	if info.Size() == 0 {
		return false, false, fmt.Errorf("config %q is empty", target)
	}
	existing, err := os.ReadFile(target)
	if err != nil {
		return false, false, fmt.Errorf("read config %q: %w", target, err)
	}
	if bytes.Equal(existing, current) {
		return false, false, writeManagedConfigVersion(versionPath)
	}

	legacy, legacyErr := legacyManagedConfig(current)
	if legacyErr == nil && bytes.Equal(existing, legacy) {
		if err := writeManagedConfig(target, current); err != nil {
			return false, false, err
		}
		if err := writeManagedConfigVersion(versionPath); err != nil {
			return false, true, err
		}
		return false, true, nil
	}
	if bytes.Contains(existing, []byte("GEOIP,CN")) {
		return false, false, fmt.Errorf("custom config uses GEOIP,CN and was preserved; replace it with the packaged local ChinaIp rule before retrying")
	}
	return false, false, nil
}

func legacyManagedConfig(current []byte) ([]byte, error) {
	text := string(current)
	if strings.Count(text, managedChinaIPProvider) != 1 || strings.Count(text, managedChinaIPRule) != 1 {
		return nil, errors.New("packaged config is missing the managed ChinaIp rule")
	}
	text = strings.Replace(text, managedChinaIPProvider, "", 1)
	text = strings.Replace(text, managedChinaIPRule, legacyChinaIPRule, 1)
	return []byte(text), nil
}

func validateManagedConfigVersion(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect managed config version: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("managed config version marker is not a regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read managed config version: %w", err)
	}
	if string(content) != managedConfigVersion+"\n" {
		return fmt.Errorf("unsupported managed config version %q", strings.TrimSpace(string(content)))
	}
	return nil
}

func writeManagedConfig(path string, content []byte) error {
	return atomicWrite(path, 0o644, func(output *os.File) error {
		_, err := output.Write(content)
		return err
	})
}

func writeManagedConfigVersion(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect managed config version: %w", err)
	}
	return atomicWrite(path, 0o644, func(output *os.File) error {
		_, err := output.WriteString(managedConfigVersion + "\n")
		return err
	})
}

func enforceServerSettings(path string) (bool, error) {
	content, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read settings %q: %w", path, err)
	}

	lines := make([]string, 0)
	if len(content) > 0 {
		lines = strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	}
	indexes := make(map[string]int, len(serverSettings))
	for _, setting := range serverSettings {
		indexes[setting.key] = -1
	}
	for index, line := range lines {
		for _, setting := range serverSettings {
			if !strings.HasPrefix(line, setting.key) {
				continue
			}
			if indexes[setting.key] >= 0 {
				return false, fmt.Errorf("multiple %s entries in %q", strings.TrimSuffix(setting.key, "="), path)
			}
			indexes[setting.key] = index
		}
	}
	changed := false
	for _, setting := range serverSettings {
		expected := setting.key + setting.value
		index := indexes[setting.key]
		if index >= 0 {
			if lines[index] != expected {
				lines[index] = expected
				changed = true
			}
			continue
		}
		lines = append(lines, expected)
		changed = true
	}
	if !changed {
		return false, nil
	}

	settings := strings.Join(lines, "\n") + "\n"
	err = atomicWrite(path, 0o600, func(output *os.File) error {
		if _, writeErr := output.WriteString(settings); writeErr != nil {
			return fmt.Errorf("write settings %q: %w", path, writeErr)
		}
		return nil
	})
	return changed, err
}

func atomicWrite(path string, mode os.FileMode, write func(*os.File) error) (resultErr error) {
	temp, err := os.CreateTemp(filepath.Dir(path), ".mohomo-docker-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %q: %w", path, err)
	}
	tempPath := temp.Name()
	closed := false
	defer func() {
		if !closed {
			if closeErr := temp.Close(); resultErr == nil && closeErr != nil {
				resultErr = fmt.Errorf("close temporary file for %q: %w", path, closeErr)
			}
		}
		if resultErr != nil {
			_ = os.Remove(tempPath)
		}
	}()

	if err := temp.Chmod(mode); err != nil {
		return fmt.Errorf("chmod temporary file for %q: %w", path, err)
	}
	if err := write(temp); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync temporary file for %q: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary file for %q: %w", path, err)
	}
	closed = true
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace %q atomically: %w", path, err)
	}
	return nil
}
