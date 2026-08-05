package service

import (
	"context"
	"testing"
	"time"

	"github.com/dushixiang/pika/internal/models"
	"github.com/dushixiang/pika/internal/protocol"
	"github.com/dushixiang/pika/internal/repo"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func TestHandleShellCommandResponsePersistsOutputAndCompletion(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&models.CommandTask{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	service := &AgentService{logger: zap.NewNop(), AgentRepo: repo.NewAgentRepo(db)}
	ctx := context.Background()
	task := &models.CommandTask{
		ID: "command-1", AgentID: "agent-1", Command: "printf hello", Status: "pending", TimeoutSeconds: 60, CreatedAt: 1,
	}
	if err := service.CreateCommandTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := service.HandleCommandResponse(ctx, task.AgentID, &protocol.CommandResponse{
		ID: task.ID, Type: "shell_exec", Status: "running", Output: "hello",
	}); err != nil {
		t.Fatalf("handle running response: %v", err)
	}
	exitCode := 0
	if err := service.HandleCommandResponse(ctx, task.AgentID, &protocol.CommandResponse{
		ID: task.ID, Type: "shell_exec", Status: "success", ExitCode: &exitCode,
	}); err != nil {
		t.Fatalf("handle success response: %v", err)
	}

	got, err := service.GetCommandTask(ctx, task.AgentID, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status != "success" || got.Output != "hello" || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("unexpected task: %#v", got)
	}
	if got.StartedAt == 0 || got.FinishedAt == 0 {
		t.Fatalf("expected task timestamps to be populated: %#v", got)
	}

	stale := &models.CommandTask{
		ID: "command-stale", AgentID: "agent-1", Command: "sleep 10", Status: "running", TimeoutSeconds: 1,
		CreatedAt: time.Now().Add(-time.Minute).UnixMilli(),
	}
	if err := service.CreateCommandTask(ctx, stale); err != nil {
		t.Fatalf("create stale task: %v", err)
	}
	if _, err := service.ListCommandTasks(ctx, stale.AgentID); err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	gotStale, err := service.GetCommandTask(ctx, stale.AgentID, stale.ID)
	if err != nil {
		t.Fatalf("get stale task: %v", err)
	}
	if gotStale.Status != "error" || gotStale.FinishedAt == 0 {
		t.Fatalf("expected stale task to be expired: %#v", gotStale)
	}

	cancellable := &models.CommandTask{
		ID: "command-cancel", AgentID: "agent-1", Command: "sleep 10", Status: "running", TimeoutSeconds: 60,
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := service.CreateCommandTask(ctx, cancellable); err != nil {
		t.Fatalf("create cancellable task: %v", err)
	}
	transitioned, err := service.AgentRepo.TransitionCommandTaskStatus(
		ctx, cancellable.AgentID, cancellable.ID, []string{"pending", "running"}, "cancelling",
	)
	if err != nil || !transitioned {
		t.Fatalf("transition task to cancelling: transitioned=%v err=%v", transitioned, err)
	}
	transitioned, err = service.AgentRepo.TransitionCommandTaskStatus(
		ctx, cancellable.AgentID, cancellable.ID, []string{"pending", "running"}, "cancelling",
	)
	if err != nil || transitioned {
		t.Fatalf("expected repeated transition to be rejected: transitioned=%v err=%v", transitioned, err)
	}
}
