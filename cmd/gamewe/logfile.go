// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"sync"
)

// rotatingLog appends to a file and, once it would grow past max bytes,
// renames it to path+".1" (replacing the older one) and starts a new one:
// the log of the Windows service, which has no journal.
type rotatingLog struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func openRotatingLog(path string, max int64) (*rotatingLog, error) {
	l := &rotatingLog{path: path, max: max}
	return l, l.open()
}

func (l *rotatingLog) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, fi.Size()
	return nil
}

func (l *rotatingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size > 0 && l.size+int64(len(p)) > l.max {
		l.f.Close()
		renamed := os.Rename(l.path, l.path+".1") == nil
		if err := l.open(); err != nil {
			return 0, err // tried again with the next write
		}
		if !renamed { // e.g. open in a viewer on Windows: try again after max more bytes
			l.size = 0
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

func (l *rotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
