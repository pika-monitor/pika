package sshmonitor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveHookSymlinkRemovesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "agent")
	link := filepath.Join(dir, "pika-agent")
	if err := os.WriteFile(target, []byte("test"), 0755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	if err := removeHookSymlink(link); err != nil {
		t.Fatalf("removeHookSymlink() error = %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("hook symlink still exists: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("symlink target was removed: %v", err)
	}
}

func TestRemoveHookSymlinkKeepsRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pika-agent")
	if err := os.WriteFile(path, []byte("test"), 0755); err != nil {
		t.Fatalf("write regular file: %v", err)
	}

	if err := removeHookSymlink(path); err != nil {
		t.Fatalf("removeHookSymlink() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("regular executable was removed: %v", err)
	}
}
