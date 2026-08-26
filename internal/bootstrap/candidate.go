package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const maxSecretSize = 4096

type CandidateConfig struct {
	SecretPath   string
	DataDir      string
	TemplatePath string
	MihomoBinary string
	Client       *http.Client
}

// PublishCandidate performs one Stage 1 update. Starting or reloading Mihomo is
// deliberately left to the lifecycle stage.
func PublishCandidate(ctx context.Context, config CandidateConfig) error {
	dataDir := filepath.Clean(config.DataDir)
	if !filepath.IsAbs(dataDir) || dataDir == string(filepath.Separator) {
		return fmt.Errorf("unsafe data directory %q", config.DataDir)
	}
	if err := ensureDirectory(dataDir); err != nil {
		return err
	}
	generations := filepath.Join(dataDir, "generations")
	if err := ensureDirectory(generations); err != nil {
		return err
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
		return fmt.Errorf("create candidate generation: %w", err)
	}
	if err := os.Chmod(candidate, 0o700); err != nil {
		_ = os.RemoveAll(candidate)
		return fmt.Errorf("secure candidate generation: %w", err)
	}
	defer os.RemoveAll(candidate)

	configPath := filepath.Join(candidate, "config.yaml")
	if err := writePrivateFile(configPath, generated); err != nil {
		return fmt.Errorf("write candidate config: %w", err)
	}
	if err := writePrivateFile(filepath.Join(candidate, "subscription.yaml"), subscription); err != nil {
		return fmt.Errorf("write candidate subscription: %w", err)
	}
	if err := validateMihomoConfig(ctx, config.MihomoBinary, candidate, configPath); err != nil {
		return errors.New("candidate configuration failed Mihomo validation")
	}

	slot := filepath.Join(dataDir, filepath.FromSlash(next))
	if err := os.RemoveAll(slot); err != nil {
		return fmt.Errorf("clear inactive generation: %w", err)
	}
	if err := os.Rename(candidate, slot); err != nil {
		return fmt.Errorf("publish candidate generation: %w", err)
	}
	if err := syncDirectory(generations); err != nil {
		return err
	}
	if err := replaceSymlink(filepath.Join(dataDir, "last-good"), next); err != nil {
		return err
	}
	return syncDirectory(dataDir)
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
