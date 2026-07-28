package service

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dushixiang/pika/pkg/agent/utils"
)

const legacyMetricsBufferDBName = "metrics_buffer.db"

func cleanupLegacyMetricsBuffer() error {
	path := filepath.Join(utils.GetSafeHomeDir(), ".pika", legacyMetricsBufferDBName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove legacy metrics buffer failed: %w", err)
	}
	return nil
}
