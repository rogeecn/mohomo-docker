package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

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

func validateMihomoConfig(ctx context.Context, binary, runtimeDir, configPath string) error {
	command := exec.CommandContext(ctx, binary, "-t", "-d", runtimeDir, "-f", configPath)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return errors.New("Mihomo validation failed")
	}
	return nil
}

func serviceCommand(ctx context.Context, binary, runtimeDir, configPath string) *exec.Cmd {
	command := exec.CommandContext(ctx, binary, "-d", runtimeDir, "-f", configPath)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	command.WaitDelay = 10 * time.Second
	return command
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
