package bootstrap

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const operatingModeKey = "OPERATING_MODE="

var runtimeDirectories = []string{
	"bin",
	".ssclash",
	"configs",
	"local-rules",
	"rule-providers",
	"proxy-providers",
	"subscriptions",
	"ui",
}

type Config struct {
	Root         string
	CoreSource   string
	ConfigSource string
}

type Result struct {
	CoreInitialized   bool
	ConfigInitialized bool
	ServerModeChanged bool
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

	var err error
	result.CoreInitialized, err = copyIfAbsent(config.CoreSource, filepath.Join(root, "bin", "clash"), 0o755)
	if err != nil {
		return result, fmt.Errorf("initialize Mihomo core: %w", err)
	}
	result.ConfigInitialized, err = copyIfAbsent(config.ConfigSource, filepath.Join(root, "config.yaml"), 0o644)
	if err != nil {
		return result, fmt.Errorf("initialize config: %w", err)
	}
	result.ServerModeChanged, err = enforceServerMode(filepath.Join(root, ".ssclash", "settings"))
	if err != nil {
		return result, fmt.Errorf("enforce server mode: %w", err)
	}

	return result, nil
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

func enforceServerMode(path string) (bool, error) {
	content, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read settings %q: %w", path, err)
	}

	lines := make([]string, 0)
	if len(content) > 0 {
		lines = strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	}
	modeIndex := -1
	for index, line := range lines {
		if strings.HasPrefix(line, operatingModeKey) {
			if modeIndex >= 0 {
				return false, fmt.Errorf("multiple OPERATING_MODE entries in %q", path)
			}
			modeIndex = index
		}
	}
	changed := modeIndex < 0 || lines[modeIndex] != operatingModeKey+"server"
	if modeIndex >= 0 {
		lines[modeIndex] = operatingModeKey + "server"
	} else {
		lines = append(lines, operatingModeKey+"server")
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
