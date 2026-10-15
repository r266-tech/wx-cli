// Package filelock provides process-scoped filesystem locks released by the OS
// if a writer exits. Lock files are persistent; removing one can split a lock.
package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

func Acquire(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := filepath.Base(path)
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("lock path must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		f, err = root.OpenFile(name, os.O_RDWR, 0)
	}
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	pathInfo, pathErr := root.Lstat(name)
	if err != nil || pathErr != nil || !info.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, pathInfo) {
		_ = f.Close()
		return nil, errors.New("lock path changed during validation")
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	unlock, err := lock(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			unlock()
			_ = f.Close()
		})
	}, nil
}
