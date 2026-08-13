// Package fsutil provides filesystem helpers shared across iptest-web.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes body to path by first writing a temporary file in the
// same directory and then renaming it into place. This keeps the destination
// intact if the process crashes mid-write: readers never observe a truncated or
// half-written file. Missing parent directories are created automatically.
//
// perm is applied to the temporary file before the rename. On platforms where
// file permissions are not enforced (notably Windows) it is ignored.
func WriteFileAtomic(path string, body []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("替换文件失败: %w", err)
	}
	return nil
}
