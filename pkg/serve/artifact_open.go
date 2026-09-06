package serve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// openArtifactPath opens an already-canonical absolute path for reading
// without following a symlink in ANY component, and returns the descriptor
// plus its verified regular-file info.
//
// os.OpenRoot(filepath.Dir(path)) is not enough: that call itself walks the
// parents and would happily follow one that has since been replaced. So the
// walk starts at the volume root and descends one component at a time,
// checking each child with Lstat before opening it and confirming with
// SameFile that the directory actually opened is the one inspected — that
// pair closes the race where a component is swapped in between.
//
// The caller owns the returned file and must close it. The path is never
// reopened afterwards: size, MIME and content all come from this descriptor,
// so what is measured is what is served.
func openArtifactPath(path string) (*os.File, os.FileInfo, error) {
	if !filepath.IsAbs(path) {
		return nil, nil, fmt.Errorf("artifact path is not absolute")
	}
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	rest := strings.TrimPrefix(clean, volume)
	rootPath := volume + string(filepath.Separator)

	components := strings.Split(strings.Trim(rest, string(filepath.Separator)), string(filepath.Separator))
	if len(components) == 0 || components[0] == "" {
		return nil, nil, fmt.Errorf("artifact path has no file component")
	}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, nil, fmt.Errorf("artifact source is unavailable")
	}
	defer func() { _ = root.Close() }()

	for _, dir := range components[:len(components)-1] {
		info, err := root.Lstat(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("artifact source is unavailable")
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, nil, fmt.Errorf("artifact source is unavailable")
		}
		child, err := root.OpenRoot(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("artifact source is unavailable")
		}
		opened, err := child.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			_ = child.Close()
			return nil, nil, fmt.Errorf("artifact source is unavailable")
		}
		_ = root.Close()
		root = child
	}

	leaf := components[len(components)-1]
	f, err := openArtifactLeaf(root, leaf)
	if err != nil {
		return nil, nil, fmt.Errorf("artifact source is unavailable")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("artifact source is unavailable")
	}
	return f, info, nil
}
