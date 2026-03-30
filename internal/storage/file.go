package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	if err := os.MkdirAll(d.root, 0o750); err != nil {
		return fmt.Errorf("create backup location: %w", err)
	}
	probe, err := os.CreateTemp(d.root, ".probe-*")
	if err != nil {
		return fmt.Errorf("create probe file: %w", err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
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
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o750); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open source artifact: %w", err)
	}
	defer source.Close()
	return writeAtomically(ctx, cleanPath, func(destination *os.File) error {
		_, err := io.Copy(destination, source)
		return err
	})
}

func (d *FileDestination) UploadBytes(ctx context.Context, name string, content []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cleanPath, err := d.resolve(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o750); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	return writeAtomically(ctx, cleanPath, func(destination *os.File) error {
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
	walkErr := filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
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
		rel, err := filepath.Rel(d.root, path)
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
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
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

func writeAtomically(ctx context.Context, path string, write func(*os.File) error) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return fmt.Errorf("create temp destination: %w", err)
	}
	tempName := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
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
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("move destination content into place: %w", err)
	}
	return nil
}
