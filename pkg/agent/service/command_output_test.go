package service

import (
	"strings"
	"testing"
)

func TestCommandOutputWriterStreamsAndLimitsOutput(t *testing.T) {
	var output strings.Builder
	state := &commandOutputState{send: func(stream, chunk string) {
		if stream != "stdout" {
			t.Fatalf("unexpected stream: %s", stream)
		}
		output.WriteString(chunk)
	}}
	writer := &commandOutputWriter{stream: "stdout", state: state}

	input := strings.Repeat("x", maxShellCommandOutputBytes+128)
	n, err := writer.Write([]byte(input))
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if n != len(input) {
		t.Fatalf("write returned %d, want %d", n, len(input))
	}
	if output.Len() != maxShellCommandOutputBytes {
		t.Fatalf("captured %d bytes, want %d", output.Len(), maxShellCommandOutputBytes)
	}
	if !state.truncated {
		t.Fatal("expected output to be marked truncated")
	}
}
