package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dushixiang/pika/pkg/agent/config"
)

func TestRemoveAgentDataDefaultConfig(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	dataDir := config.GetDataDir()
	configPath := config.GetDefaultConfigPath()
	writeTestFile(t, configPath)
	writeTestFile(t, filepath.Join(dataDir, "agent.id"))
	writeTestFile(t, filepath.Join(dataDir, "logs", "agent.log"))
	writeTestFile(t, filepath.Join(dataDir, "metrics_buffer.db"))

	if err := removeAgentData(configPath); err != nil {
		t.Fatalf("removeAgentData() error = %v", err)
	}

	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("data directory still exists after uninstall: %v", err)
	}
}

func TestRemoveAgentDataEmptyConfigPath(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	writeTestFile(t, config.GetDefaultConfigPath())

	if err := removeAgentData(""); err != nil {
		t.Fatalf("removeAgentData() error = %v", err)
	}
	if _, err := os.Stat(config.GetDataDir()); !os.IsNotExist(err) {
		t.Fatalf("data directory still exists after uninstall: %v", err)
	}
}

func TestRemoveAgentDataCustomConfig(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	customDir := t.TempDir()
	configPath := filepath.Join(customDir, "agent.yaml")
	otherPath := filepath.Join(customDir, "keep.txt")
	writeTestFile(t, configPath)
	writeTestFile(t, otherPath)
	writeTestFile(t, filepath.Join(config.GetDataDir(), "agent.id"))

	if err := removeAgentData(configPath); err != nil {
		t.Fatalf("removeAgentData() error = %v", err)
	}

	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("custom config still exists after uninstall: %v", err)
	}
	if _, err := os.Stat(config.GetDataDir()); !os.IsNotExist(err) {
		t.Fatalf("data directory still exists after uninstall: %v", err)
	}
	if _, err := os.Stat(otherPath); err != nil {
		t.Fatalf("unrelated file was removed: %v", err)
	}
}

func TestCleanupAgentArtifactsRemovesDataWhenSSHCleanupFails(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	dataDir := config.GetDataDir()
	writeTestFile(t, config.GetDefaultConfigPath())
	writeTestFile(t, filepath.Join(dataDir, "logs", "agent.log"))

	sshErr := errors.New("SSH cleanup failed")
	err := cleanupAgentArtifacts(config.GetDefaultConfigPath(), func() error {
		return sshErr
	})
	if !errors.Is(err, sshErr) {
		t.Fatalf("cleanupAgentArtifacts() error = %v, want wrapped SSH error", err)
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("data directory still exists after SSH cleanup failure: %v", err)
	}
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create parent directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
}
