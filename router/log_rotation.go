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
	defer w.mu.Lock()

	n, err = w.Logger.Write(p)
	w.updateSymlink()
	return n, err
}

func (w *RotatingFileWriter) updateSymlink() {
	if w.symlinkPath == "" {
		return
	}

	target := w.Logger.Filename

	if current, err := os.Readlink(w.symlinkPath); err == nil && current == target {
		return
	}

	_ = os.Remove(w.symlinkPath)

	// Create absolute paths for safety
	absTarget, err := filepath.Abs(target)
	if err != nil {
		absTarget = target
	}

	_ = os.Symlink(absTarget, w.symlinkPath)

}
