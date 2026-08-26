package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	maxSecretSize        = 4096
	maxSubscriptionSize  = 16 << 20
	defaultControllerURL = "http://127.0.0.1:9090"
)

var errPersistence = errors.New("candidate persistence failure")

type CandidateConfig struct {
	SecretPath      string
	DataDir         string
	TemplatePath    string
	MihomoBinary    string
	Client          *http.Client
	replaceLastGood func(string, string) error
	directorySync   func(string) error
}

type LifecycleConfig struct {
	Candidate      CandidateConfig
	ControllerURL  string
	UpdateInterval time.Duration
	Trigger        <-chan os.Signal
}

// PublishCandidate performs one Stage 1 update. Starting or reloading Mihomo is
// deliberately left to the lifecycle stage.
func PublishCandidate(ctx context.Context, config CandidateConfig) error {
	return withDataDirLock(config.DataDir, func(dataDir string) error {
		config.DataDir = dataDir
		return publishCandidateLocked(ctx, config)
	})
}

func publishCandidateLocked(ctx context.Context, config CandidateConfig) error {
	dataDir := config.DataDir
	generations := filepath.Join(dataDir, "generations")
	if err := ensureDirectory(generations); err != nil {
		return persistenceError("prepare generations directory", err)
	}
	for path, label := range map[string]string{
		config.TemplatePath: "Mihomo template",
		config.MihomoBinary: "Mihomo binary",
	} {
		if err := validateSource(path, label); err != nil {
			return err
		}
	}

	endpoint, err := readSubscriptionSecret(config.SecretPath)
	if err != nil {
		return err
	}
	client := config.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	subscription, err := fetchSubscription(ctx, client, endpoint)
	if err != nil {
		return err
	}
	subscription, err = normalizeSubscription(subscription)
	if err != nil {
		return err
	}
	template, err := os.ReadFile(config.TemplatePath)
	if err != nil {
		return errors.New("read Mihomo template")
	}
	generated, err := generateConfig(template)
	if err != nil {
		return err
	}

	current, err := currentGeneration(filepath.Join(dataDir, "last-good"))
	if err != nil {
		return err
	}
	next := "generations/a"
	if current == next {
		next = "generations/b"
	}
	candidate, err := os.MkdirTemp(generations, ".candidate-")
	if err != nil {
		return persistenceError("create candidate generation", err)
	}
	if err := os.Chmod(candidate, 0o700); err != nil {
		_ = os.RemoveAll(candidate)
		return persistenceError("secure candidate generation", err)
	}
	defer os.RemoveAll(candidate)

	configPath := filepath.Join(candidate, "config.yaml")
	if err := writePrivateFile(configPath, generated); err != nil {
		return persistenceError("write candidate config", err)
	}
	if err := writePrivateFile(filepath.Join(candidate, "subscription.yaml"), subscription); err != nil {
		return persistenceError("write candidate subscription", err)
	}
	if err := validateMihomoConfig(ctx, config.MihomoBinary, candidate, configPath); err != nil {
		return errors.New("candidate configuration failed Mihomo validation")
	}

	slot := filepath.Join(dataDir, filepath.FromSlash(next))
	if err := os.RemoveAll(slot); err != nil {
		return persistenceError("clear inactive generation", err)
	}
	if err := os.Rename(candidate, slot); err != nil {
		return persistenceError("publish candidate generation", err)
	}
	if err := config.syncDirectory(generations); err != nil {
		return persistenceError("sync generations directory", err)
	}
	if err := config.replacePointer(filepath.Join(dataDir, "last-good"), next); err != nil {
		return persistenceError("publish last-good pointer", err)
	}
	if err := config.syncDirectory(dataDir); err != nil {
		rollbackErr := restoreLastGood(config, dataDir, current)
		return errors.Join(persistenceError("sync data directory", err), rollbackErr)
	}
	return nil
}

// Run starts the last known-good configuration and owns Mihomo until ctx ends.
func Run(ctx context.Context, config LifecycleConfig) error {
	if config.UpdateInterval <= 0 {
		return errors.New("update interval must be positive")
	}
	controllerURL := strings.TrimRight(config.ControllerURL, "/")
	if controllerURL == "" {
		controllerURL = defaultControllerURL
	}
	if err := validateControllerURL(controllerURL); err != nil {
		return err
	}

	var generation string
	valid := withDataDirLock(config.Candidate.DataDir, func(dataDir string) error {
		config.Candidate.DataDir = dataDir
		var err error
		generation, err = validLastGoodLocked(ctx, config.Candidate)
		return err
	})
	warm := valid == nil
	if !warm {
		if err := PublishCandidate(ctx, config.Candidate); err != nil {
			return fmt.Errorf("cold-start candidate failed: %w", err)
		}
		valid = withDataDirLock(config.Candidate.DataDir, func(dataDir string) error {
			config.Candidate.DataDir = dataDir
			var err error
			generation, err = validLastGoodLocked(ctx, config.Candidate)
			return err
		})
		if valid != nil {
			return fmt.Errorf("published candidate is invalid: %w", valid)
		}
	}

	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	mihomo := serviceCommand(serviceCtx, config.Candidate.MihomoBinary, generation, filepath.Join(generation, "config.yaml"))
	if err := mihomo.Start(); err != nil {
		return fmt.Errorf("start Mihomo: %w", err)
	}
	exit := make(chan error, 1)
	go func() { exit <- mihomo.Wait() }()
	client := config.Candidate.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if err := waitForController(ctx, client, controllerURL, exit); err != nil {
		return err
	}
	log.Printf("bootstrap: Mihomo started config=%s update_interval=%s", filepath.Base(generation), config.UpdateInterval)
	failClosed := func(err error) error {
		cancel()
		<-exit
		return fmt.Errorf("fatal update stopped Mihomo: %w", err)
	}

	if warm {
		if err := updateAndReload(ctx, client, controllerURL, config.Candidate); err != nil {
			return failClosed(err)
		}
	}
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
				return errors.New("Mihomo exited")
			}
			return fmt.Errorf("Mihomo exited: %w", err)
		case <-ticker.C:
			if err := updateAndReload(ctx, client, controllerURL, config.Candidate); err != nil {
				return failClosed(err)
			}
		case <-config.Trigger:
			if err := updateAndReload(ctx, client, controllerURL, config.Candidate); err != nil {
				return failClosed(err)
			}
		}
	}
}

func validLastGoodLocked(ctx context.Context, config CandidateConfig) (string, error) {
	target, err := currentGeneration(filepath.Join(config.DataDir, "last-good"))
	if err != nil {
		return "", err
	}
	if target == "" {
		return "", errors.New("last-good is missing")
	}
	directory := filepath.Join(config.DataDir, filepath.FromSlash(target))
	for path, label := range map[string]string{
		filepath.Join(directory, "config.yaml"):       "last-good config",
		filepath.Join(directory, "subscription.yaml"): "last-good subscription",
	} {
		if err := validateSource(path, label); err != nil {
			return "", err
		}
	}
	if err := validateMihomoConfig(ctx, config.MihomoBinary, directory, filepath.Join(directory, "config.yaml")); err != nil {
		return "", errors.New("last-good failed Mihomo validation")
	}
	return directory, nil
}

func updateAndReload(ctx context.Context, client *http.Client, controllerURL string, config CandidateConfig) error {
	return withDataDirLock(config.DataDir, func(dataDir string) error {
		config.DataDir = dataDir
		previous, err := currentGeneration(filepath.Join(dataDir, "last-good"))
		if err != nil || previous == "" {
			return errors.Join(errors.New("last-good is unavailable during update"), err)
		}
		if err := publishCandidateLocked(ctx, config); err != nil {
			if errors.Is(err, errPersistence) {
				return err
			}
			log.Printf("bootstrap: update rejected; keeping last-good: %v", err)
			return nil
		}
		next, err := currentGeneration(filepath.Join(dataDir, "last-good"))
		if err == nil {
			err = reloadMihomo(ctx, client, controllerURL, filepath.Join(dataDir, filepath.FromSlash(next), "config.yaml"))
		}
		if err == nil {
			log.Print("bootstrap: configuration updated and reloaded")
			return nil
		}
		if rollbackErr := config.replacePointer(filepath.Join(dataDir, "last-good"), previous); rollbackErr != nil {
			return fmt.Errorf("restore last-good pointer after reload failure: %w", rollbackErr)
		}
		if rollbackErr := config.syncDirectory(dataDir); rollbackErr != nil {
			return fmt.Errorf("persist restored last-good pointer after reload failure: %w", rollbackErr)
		}
		if rollbackErr := reloadMihomo(ctx, client, controllerURL, filepath.Join(dataDir, filepath.FromSlash(previous), "config.yaml")); rollbackErr != nil {
			return fmt.Errorf("reload restored last-good after reload failure: %w", rollbackErr)
		}
		log.Printf("bootstrap: reload rejected; restored and reloaded last-good: %v", err)
		return nil
	})
}

func reloadMihomo(ctx context.Context, client *http.Client, controllerURL, configPath string) error {
	body, err := json.Marshal(map[string]string{"path": configPath})
	if err != nil {
		return errors.New("encode Mihomo reload request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, controllerURL+"/configs?force=true", bytes.NewReader(body))
	if err != nil {
		return errors.New("create Mihomo reload request")
	}
	request.Header.Set("Content-Type", "application/json")
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

func waitForController(ctx context.Context, client *http.Client, controllerURL string, exit <-chan error) error {
	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, controllerURL+"/version", nil)
		if response, err := client.Do(request); err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-exit:
			if err == nil {
				return errors.New("Mihomo exited before controller became ready")
			}
			return fmt.Errorf("Mihomo exited before controller became ready: %w", err)
		case <-timeout.C:
			return errors.New("Mihomo controller did not become ready")
		case <-ticker.C:
		}
	}
}

func validateControllerURL(raw string) error {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return errors.New("controller URL must be an absolute HTTP(S) URL")
	}
	return nil
}

func withDataDirLock(dataDir string, operation func(string) error) error {
	dataDir = filepath.Clean(dataDir)
	if !filepath.IsAbs(dataDir) || dataDir == string(filepath.Separator) {
		return fmt.Errorf("unsafe data directory %q", dataDir)
	}
	if err := ensureDirectory(dataDir); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dataDir, ".bootstrap.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open data directory lock: %w", err)
	}
	if err := lock.Chmod(0o600); err != nil {
		_ = lock.Close()
		return fmt.Errorf("secure data directory lock: %w", err)
	}
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = lock.Close()
		return fmt.Errorf("lock data directory: %w", err)
	}

	operationErr := operation(dataDir)
	unlockErr := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	closeErr := lock.Close()
	if unlockErr != nil {
		unlockErr = fmt.Errorf("unlock data directory: %w", unlockErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close data directory lock: %w", closeErr)
	}
	return errors.Join(operationErr, unlockErr, closeErr)
}

func ensureDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create data directory: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect data directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("data path %q is not a directory", path)
	}
	return nil
}

func (config CandidateConfig) replacePointer(path, target string) error {
	if config.replaceLastGood != nil {
		return config.replaceLastGood(path, target)
	}
	return replaceSymlink(path, target)
}

func (config CandidateConfig) syncDirectory(path string) error {
	if config.directorySync != nil {
		return config.directorySync(path)
	}
	return syncDirectory(path)
}

func restoreLastGood(config CandidateConfig, dataDir, target string) error {
	path := filepath.Join(dataDir, "last-good")
	var err error
	if target == "" {
		err = os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	} else {
		err = config.replacePointer(path, target)
	}
	if err != nil {
		return persistenceError("restore last-good pointer", err)
	}
	if err := config.syncDirectory(dataDir); err != nil {
		return persistenceError("sync restored last-good pointer", err)
	}
	return nil
}

func persistenceError(action string, err error) error {
	return fmt.Errorf("%w: %s: %w", errPersistence, action, err)
}

func readSubscriptionSecret(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", errors.New("read subscription secret")
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxSecretSize {
		return "", errors.New("subscription secret must be a non-empty regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("read subscription secret")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.New("subscription secret changed while being read")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxSecretSize+1))
	if err != nil || len(content) > maxSecretSize {
		return "", errors.New("read subscription secret")
	}
	raw := strings.TrimSpace(string(content))
	if strings.ContainsAny(raw, "\r\n") {
		return "", errors.New("subscription secret must contain one URL")
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return "", errors.New("subscription secret must contain one absolute HTTP(S) URL")
	}
	return raw, nil
}

func fetchSubscription(ctx context.Context, client *http.Client, endpoint string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("create subscription request")
	}
	request.Header.Set("Accept", "application/yaml, text/yaml, text/plain")
	request.Header.Set("User-Agent", "mihomo")
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("subscription request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("subscription endpoint returned HTTP %d", response.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, maxSubscriptionSize+1))
	if err != nil {
		return nil, errors.New("read subscription response")
	}
	if len(content) == 0 || len(content) > maxSubscriptionSize {
		return nil, errors.New("subscription response is empty or too large")
	}
	return content, nil
}

func normalizeSubscription(content []byte) ([]byte, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil || len(document.Content) != 1 {
		return nil, errors.New("subscription YAML is invalid")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("subscription YAML must contain one document")
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("subscription YAML must be a mapping")
	}
	var proxies *yaml.Node
	for index := 0; index < len(root.Content); index += 2 {
		if root.Content[index].Value != "proxies" {
			continue
		}
		if proxies != nil {
			return nil, errors.New("subscription YAML contains duplicate proxies fields")
		}
		proxies = root.Content[index+1]
	}
	if proxies == nil || proxies.Kind != yaml.SequenceNode || len(proxies.Content) == 0 {
		return nil, errors.New("subscription YAML must contain a non-empty proxies list")
	}
	for _, proxy := range proxies.Content {
		if proxy.Kind != yaml.MappingNode {
			return nil, errors.New("subscription YAML contains an invalid proxy")
		}
	}
	normalized := yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{
		Kind: yaml.MappingNode,
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "proxies"},
			proxies,
		},
	}}}
	return yaml.Marshal(&normalized)
}

func generateConfig(content []byte) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil || len(document.Content) != 1 {
		return nil, errors.New("Mihomo template is invalid")
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("Mihomo template must be a mapping")
	}
	controller := mappingValue(root, "external-controller")
	if controller == nil || controller.Kind != yaml.ScalarNode {
		return nil, errors.New("Mihomo template is missing external-controller")
	}
	controller.Tag = "!!str"
	controller.Value = "0.0.0.0:9090"
	return yaml.Marshal(&document)
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func currentGeneration(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect last-good generation: %w", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", errors.New("last-good must be a managed symlink")
	}
	target, err := os.Readlink(path)
	if err != nil {
		return "", fmt.Errorf("read last-good generation: %w", err)
	}
	if target != "generations/a" && target != "generations/b" {
		return "", fmt.Errorf("last-good has unexpected target %q", target)
	}
	return target, nil
}

func writePrivateFile(path string, content []byte) error {
	return atomicWrite(path, 0o600, func(output *os.File) error {
		_, err := output.Write(content)
		return err
	})
}

func replaceSymlink(path, target string) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".last-good-")
	if err != nil {
		return fmt.Errorf("create last-good pointer: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("close last-good pointer: %w", err)
	}
	if err := os.Remove(temporaryPath); err != nil {
		return fmt.Errorf("prepare last-good pointer: %w", err)
	}
	defer os.Remove(temporaryPath)
	if err := os.Symlink(target, temporaryPath); err != nil {
		return fmt.Errorf("create last-good pointer: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish last-good pointer: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open data directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync data directory: %w", err)
	}
	return nil
}
