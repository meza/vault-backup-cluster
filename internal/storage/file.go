package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type atomicFile interface {
	io.Writer
	Name() string
	Sync() error
	Close() error
}

func osCreateTempFile(dir string, pattern string) (atomicFile, error) {
	return os.CreateTemp(dir, pattern)
}

var (
	makeDir         = os.MkdirAll
	openFile        = os.Open
	createTempFile  = osCreateTempFile
	removeFile      = os.Remove
	renameFile      = os.Rename
	walkDir         = filepath.WalkDir
	relativePath    = filepath.Rel
	closeSourceFile = func(file *os.File) error { return file.Close() }
)

type Object struct {
	Name    string
	ModTime time.Time
}

type FileDestination struct {
	root string
}

func NewFileDestination(root string) *FileDestination {
	return &FileDestination{root: filepath.Clean(root)}
}

func (d *FileDestination) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := makeDir(d.root, 0o750); err != nil {
		return fmt.Errorf("create backup location: %w", err)
	}
	probe, err := createTempFile(d.root, ".probe-*")
	if err != nil {
		return fmt.Errorf("create probe file: %w", err)
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		return fmt.Errorf("close probe file: %w", err)
	}
	if err := removeFile(name); err != nil {
		return fmt.Errorf("remove probe file: %w", err)
	}
	return nil
}

func (d *FileDestination) UploadFile(ctx context.Context, name string, sourcePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cleanPath, err := d.resolve(name)
	if err != nil {
		return err
	}
	if mkdirErr := makeDir(filepath.Dir(cleanPath), 0o750); mkdirErr != nil {
		return fmt.Errorf("create destination directory: %w", mkdirErr)
	}
	source, err := openFile(sourcePath)
	if err != nil {
		return fmt.Errorf("open source artifact: %w", err)
	}
	writeErr := writeAtomically(ctx, cleanPath, func(destination io.Writer) error {
		_, err := io.Copy(destination, source)
		return err
	})
	closeErr := closeSourceFile(source)
	if writeErr != nil {
		if closeErr != nil {
			return fmt.Errorf("%w; close source artifact: %v", writeErr, closeErr)
		}
		return writeErr
	}
	if closeErr != nil {
		return fmt.Errorf("close source artifact: %w", closeErr)
	}
	return nil
}

func (d *FileDestination) UploadBytes(ctx context.Context, name string, content []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cleanPath, err := d.resolve(name)
	if err != nil {
		return err
	}
	if err := makeDir(filepath.Dir(cleanPath), 0o750); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	return writeAtomically(ctx, cleanPath, func(destination io.Writer) error {
		_, err := destination.Write(content)
		return err
	})
}

func (d *FileDestination) List(prefix string) ([]Object, error) {
	base, err := d.resolve(prefix)
	if err != nil {
		return nil, err
	}
	entries := make([]Object, 0)
	walkErr := walkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := relativePath(d.root, path)
		if err != nil {
			return err
		}
		entries = append(entries, Object{Name: filepath.ToSlash(rel), ModTime: info.ModTime().UTC()})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("list destination objects: %w", walkErr)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ModTime.After(entries[j].ModTime)
	})
	return entries, nil
}

func (d *FileDestination) Delete(name string) error {
	path, err := d.resolve(name)
	if err != nil {
		return err
	}
	if err := removeFile(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete %s: %w", name, err)
	}
	return nil
}

func (d *FileDestination) resolve(name string) (string, error) {
	clean := filepath.Clean(filepath.Join(d.root, filepath.FromSlash(name)))
	if clean == d.root {
		return clean, nil
	}
	rootWithSeparator := d.root + string(os.PathSeparator)
	if !strings.HasPrefix(clean, rootWithSeparator) {
		return "", fmt.Errorf("invalid destination path %q", name)
	}
	return clean, nil
}

func writeAtomically(ctx context.Context, path string, write func(io.Writer) error) error {
	temp, err := createTempFile(filepath.Dir(path), ".upload-*")
	if err != nil {
		return fmt.Errorf("create temp destination: %w", err)
	}
	tempName := temp.Name()
	defer func() {
		if err := temp.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			_ = err
		}
		if err := removeFile(tempName); err != nil && !os.IsNotExist(err) {
			_ = err
		}
	}()
	if err := write(temp); err != nil {
		return fmt.Errorf("write destination content: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync destination content: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close destination content: %w", err)
	}
	if err := renameFile(tempName, path); err != nil {
		return fmt.Errorf("move destination content into place: %w", err)
	}
	return nil
}
