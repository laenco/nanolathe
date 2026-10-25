package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nanolathe-gg/nanolathe/internal/save"
)

// replaceSlotSave stages an automatic slot's complete bank and sidecar before
// touching the previous pair (DESIGN_SESSIONS_AI_SAVE §5). Explicit Save Game
// keeps its retail truncate-write policy. Backups stay in the sibling staging
// directory until both new files have been published; a failed rollback keeps
// that directory for recovery instead of deleting the last valid checkpoint.
func replaceSlotSave(path string, write func(staged string) error, rename func(string, string) error) error {
	dir, err := os.MkdirTemp(filepath.Dir(path), "."+filepath.Base(path)+".")
	if err != nil {
		return slotSaveError(path, err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	staged := filepath.Join(dir, filepath.Base(path))
	if err := write(staged); err != nil {
		return slotSaveError(path, err)
	}
	files := [2]struct {
		target, staged, backup string
		hadPrevious            bool
	}{
		{target: path, staged: staged, backup: filepath.Join(dir, "previous-bank")},
		{target: save.SidecarPath(path), staged: save.SidecarPath(staged), backup: filepath.Join(dir, "previous-sidecar")},
	}
	for i := range files {
		file := &files[i]
		if info, err := os.Stat(file.staged); err != nil {
			return slotSaveError(path, err)
		} else if !info.Mode().IsRegular() {
			return slotSaveError(path, fmt.Errorf("staged file %s is not a regular file", file.staged))
		}
		file.hadPrevious, err = backupSlotFile(file.target, file.backup)
		if err != nil {
			return slotSaveError(path, err)
		}
	}
	for i := range files {
		if err := rename(files[i].staged, files[i].target); err != nil {
			var rollback error
			for j := i - 1; j >= 0; j-- {
				file := &files[j]
				if file.hadPrevious {
					rollback = errors.Join(rollback, rename(file.backup, file.target))
				} else {
					rollback = errors.Join(rollback, os.Remove(file.target))
				}
			}
			if rollback != nil {
				keep = true
				return slotSaveError(path, fmt.Errorf("%w; restoring the previous slot failed: %v; recovery files retained at %s", err, rollback, dir))
			}
			return slotSaveError(path, err)
		}
	}
	return nil
}

// backupSlotFile copies rather than moves the previous file, so a backup
// failure leaves the entire published pair intact. Copy and close errors are
// checked before either staged file can replace it.
func backupSlotFile(path, backup string) (bool, error) {
	src, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	info, err := src.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = src.Close()
		if err == nil {
			err = fmt.Errorf("previous slot file %s is not a regular file", path)
		}
		return false, err
	}
	dst, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = src.Close()
		return false, err
	}
	_, err = io.Copy(dst, src)
	return true, errors.Join(err, dst.Close(), src.Close())
}

func slotSaveError(path string, err error) error {
	return fmt.Errorf("nanolathe: automatic save not written: %w: logical path %s, providers searched [save directory], expected a complete writable bank and sidecar", err, path)
}
