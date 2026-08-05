//go:build !windows

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dushixiang/pika/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestShellCommandTimeoutKillsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()
	err := newShellCommand(ctx, "sleep 5 & wait").Run()
	if err == nil {
		t.Fatal("expected command to be cancelled")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("process group was not cancelled promptly: %s", elapsed)
	}
}

func TestHandleShellCommandStreamsOutputAndCompletion(t *testing.T) {
	serverConnCh := make(chan *websocket.Conn, 1)
	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		serverConnCh <- conn
	}))
	defer server.Close()

	agentConn, _, err := websocket.DefaultDialer.Dial(strings.Replace(server.URL, "http", "ws", 1), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer agentConn.Close()

	var serverConn *websocket.Conn
	select {
	case serverConn = <-serverConnCh:
	case err := <-serverErrCh:
		t.Fatalf("upgrade websocket: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for websocket connection")
	}
	defer serverConn.Close()

	agent := &Agent{
		activeConn:       &safeConn{conn: agentConn},
		commandCancels:   make(map[string]context.CancelFunc),
		commandCancelled: make(map[string]struct{}),
	}
	go agent.handleShellCommand(protocol.CommandRequest{
		ID: "command-1", Type: "shell_exec", Command: "printf hello", TimeoutSeconds: 5,
	})

	var output strings.Builder
	for {
		if err := serverConn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("set read deadline: %v", err)
		}
		var message protocol.InputMessage
		if err := serverConn.ReadJSON(&message); err != nil {
			t.Fatalf("read command response: %v", err)
		}
		var response protocol.CommandResponse
		if err := json.Unmarshal(message.Data, &response); err != nil {
			t.Fatalf("decode command response: %v", err)
		}
		output.WriteString(response.Output)
		if response.Status == "success" {
			if response.ExitCode == nil || *response.ExitCode != 0 {
				t.Fatalf("unexpected exit code: %#v", response.ExitCode)
			}
			break
		}
		if response.Status == "error" || response.Status == "cancelled" {
			t.Fatalf("command failed: %#v", response)
		}
	}
	if output.String() != "hello" {
		t.Fatalf("unexpected output: %q", output.String())
	}

	agent.commandResponseMu.Lock()
	_, pending := agent.pendingCommandResponses["command-1"]
	agent.commandResponseMu.Unlock()
	if !pending {
		t.Fatal("terminal response was not retained before acknowledgement")
	}
	agent.flushPendingCommandResponses()
	if err := serverConn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set replay read deadline: %v", err)
	}
	var replayed protocol.InputMessage
	if err := serverConn.ReadJSON(&replayed); err != nil {
		t.Fatalf("read replayed terminal response: %v", err)
	}
	var replayedResponse protocol.CommandResponse
	if err := json.Unmarshal(replayed.Data, &replayedResponse); err != nil {
		t.Fatalf("decode replayed terminal response: %v", err)
	}
	if replayedResponse.ID != "command-1" || replayedResponse.Status != "success" {
		t.Fatalf("unexpected replayed response: %#v", replayedResponse)
	}
	ackData, err := json.Marshal(protocol.CommandResponseAck{ID: "command-1"})
	if err != nil {
		t.Fatalf("encode acknowledgement: %v", err)
	}
	agent.handleCommandResponseAck(ackData)
	agent.commandResponseMu.Lock()
	_, pending = agent.pendingCommandResponses["command-1"]
	agent.commandResponseMu.Unlock()
	if pending {
		t.Fatal("terminal response was not removed after acknowledgement")
	}
}
