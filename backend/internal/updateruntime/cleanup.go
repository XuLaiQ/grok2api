package updateruntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Only the locked supervisor calls this before starting a child downloader.
func cleanupStaging(root string) error {
	directory, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer directory.Close()
	listing, err := directory.Open(".")
	if err != nil {
		return err
	}
	defer listing.Close()
	entries, err := listing.ReadDir(-1)
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".staging-") || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if err := directory.RemoveAll(entry.Name()); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func cleanupFailedRelease(root string, failed Activation) error {
	if failed.Directory == "" {
		return nil
	}
	if !directoryPattern.MatchString(failed.Directory) {
		return errors.New("invalid failed release directory")
	}
	for _, name := range []string{"current.json", "previous.json"} {
		var protected Activation
		if err := readJSON(filepath.Join(root, name), &protected); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if protected.Directory == failed.Directory {
			return nil
		}
	}
	directory, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer directory.Close()
	for _, name := range []string{"releases", filepath.Join("releases", failed.Directory)} {
		info, err := directory.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe failed release path: %s", name)
		}
	}
	return directory.RemoveAll(filepath.Join("releases", failed.Directory))
}
