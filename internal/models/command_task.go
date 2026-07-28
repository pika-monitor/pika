package models

// CommandTask records an administrator-triggered command executed by an agent.
type CommandTask struct {
	ID             string `gorm:"type:varchar(64);primaryKey" json:"id"`
	AgentID        string `gorm:"type:varchar(64);not null;index" json:"agentId"`
	Command        string `gorm:"type:text;not null" json:"command"`
	Status         string `gorm:"type:varchar(16);not null;index" json:"status"`
	Output         string `gorm:"type:text" json:"output,omitempty"`
	Error          string `gorm:"type:text" json:"error,omitempty"`
	ExitCode       *int   `json:"exitCode,omitempty"`
	TimeoutSeconds int    `gorm:"not null" json:"timeoutSeconds"`
	Truncated      bool   `gorm:"not null;default:false" json:"truncated"`
	StartedAt      int64  `json:"startedAt,omitempty"`
	FinishedAt     int64  `json:"finishedAt,omitempty"`
	CreatedAt      int64  `gorm:"not null;index" json:"createdAt"`
	UpdatedAt      int64  `gorm:"autoUpdateTime:milli" json:"updatedAt"`
}

func (CommandTask) TableName() string {
	return "command_tasks"
}
