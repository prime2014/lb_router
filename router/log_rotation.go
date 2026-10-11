package router

import (
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/natefinch/lumberjack.v2"
)

type RotatingFileWriter struct {
	*lumberjack.Logger
	symlinkPath string
	mu          sync.Mutex
}

func NewRotatingFileWriter(logPath, symlinkPath string, maxSizeMB, maxBackups, maxAgeDays int) *RotatingFileWriter {
	lj := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    maxSizeMB,
		MaxBackups: maxBackups,
		MaxAge:     maxAgeDays,
		Compress:   true,
	}

	w := &RotatingFileWriter{
		Logger:      lj,
		symlinkPath: symlinkPath,
	}

	w.updateSymlink()

	return w
}

func (w *RotatingFileWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n, err = w.Logger.Write(p)
	if err == nil {
		w.updateSymlink()
	}
	return n, err
}

func (w *RotatingFileWriter) updateSymlink() {
	if w.symlinkPath == "" {
		return
	}

	target := w.Logger.Filename
	absTarget, err := filepath.Abs(target)
	if err != nil {
		absTarget = target
	}

	// Single check: Read the existing symlink target
	if current, err := os.Readlink(w.symlinkPath); err == nil {
		// Resolve relative to the symlink's parent folder, not working directory
		symlinkDir := filepath.Dir(w.symlinkPath)
		absCurrent := current
		if !filepath.IsAbs(current) {
			absCurrent = filepath.Join(symlinkDir, current)
		}

		if filepath.Clean(absCurrent) == filepath.Clean(absTarget) {
			return // Symlink already points to the correct file
		}
	}

	// Update symlink atomically via temp file + rename to avoid race conditions
	tmpSymlink := w.symlinkPath + ".tmp"
	_ = os.Remove(tmpSymlink)

	if err := os.Symlink(absTarget, tmpSymlink); err == nil {
		_ = os.Rename(tmpSymlink, w.symlinkPath)
	} else {
		// Fallback for direct link if rename fails or on systems where temp symlinks fail
		_ = os.Remove(w.symlinkPath)
		_ = os.Symlink(absTarget, w.symlinkPath)
	}
}
