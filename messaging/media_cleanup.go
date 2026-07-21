package messaging

import (
	"log"
	"os"
	"path/filepath"
	"time"
)

// CleanupExpiredMedia deletes inbound media files older than retentionDays
// (by modification time) from the three inbound-media directories. It is
// best-effort: a single file's stat/remove failure is logged and does not
// stop the rest of the scan. Missing directories (an account that has never
// received that media kind) are not an error.
func CleanupExpiredMedia(retentionDays int) (deleted int, err error) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	dirs := []string{defaultInboundImageDir(), defaultInboundVideoDir(), defaultInboundFileDir()}
	for _, dir := range dirs {
		n, dirErr := cleanupDir(dir, cutoff)
		deleted += n
		if dirErr != nil {
			err = dirErr
		}
	}
	return deleted, err
}

func cleanupDir(dir string, cutoff time.Time) (deleted int, err error) {
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return 0, nil
		}
		return 0, readErr
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, statErr := entry.Info()
		if statErr != nil {
			log.Printf("[media-cleanup] stat %s failed: %v", path, statErr)
			err = statErr
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if rmErr := os.Remove(path); rmErr != nil {
			log.Printf("[media-cleanup] remove %s failed: %v", path, rmErr)
			err = rmErr
			continue
		}
		deleted++
	}
	return deleted, err
}
