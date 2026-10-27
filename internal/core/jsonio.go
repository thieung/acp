package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ReadJSON reads a JSON file. A missing file matches fs.ErrNotExist; a malformed one names the
// file and the line.
func ReadJSON(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, err := ParseJSON(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return v, nil
}

// ReadObject reads a JSON file whose top level must be an object.
func ReadObject(path string) (*Object, error) {
	v, err := ReadJSON(path)
	if err != nil {
		return nil, err
	}
	o, ok := v.(*Object)
	if !ok {
		return nil, fmt.Errorf("%s: the top level is not a JSON object", path)
	}
	return o, nil
}

// ReadObjectOr returns an empty object when the file does not exist.
func ReadObjectOr(path string) (*Object, error) {
	o, err := ReadObject(path)
	if errors.Is(err, fs.ErrNotExist) {
		return NewObject(), nil
	}
	return o, err
}

// WriteJSON writes v atomically. See WriteFileAtomic for symlinks and modes.
func WriteJSON(path string, v any, newMode fs.FileMode) error {
	data, err := MarshalJSON(v)
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, data, newMode)
}

// WriteFileAtomic writes through a symlinked path to its target, so a link stays a link. It keeps
// the target's mode (newMode for a new file), writes a unique temp file next to the target, fsyncs
// and renames.
func WriteFileAtomic(path string, data []byte, newMode fs.FileMode) error {
	target := resolveWriteTarget(path)
	mode := newMode
	if fi, err := os.Stat(target); err == nil {
		mode = fi.Mode().Perm()
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, target); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// resolveWriteTarget follows symlinks, including a dangling last link.
func resolveWriteTarget(path string) string {
	for i := 0; i < 40; i++ {
		fi, err := os.Lstat(path)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			return path
		}
		dest, err := os.Readlink(path)
		if err != nil {
			return path
		}
		if !filepath.IsAbs(dest) {
			dest = filepath.Join(filepath.Dir(path), dest)
		}
		path = dest
	}
	return path
}
