package validation

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Path confinement helpers.
//
// A confinement check compares a candidate path against a root directory that
// the caller owns. Both sides must be resolved through symlinks before the
// comparison is made:
//
//   - resolving only the candidate makes every check fail on a host that reaches
//     the tree through a link (macOS resolves /var to /private/var, /tmp to
//     /private/tmp), because the resolved candidate never shares a prefix with
//     the unresolved root;
//   - resolving neither would let a symlink planted inside the root point at an
//     arbitrary target outside it.
//
// ResolvePath and PathWithin exist so every call site resolves both sides the
// same way instead of hand-rolling the comparison.

// ResolvePath returns path in absolute, symlink-resolved form.
//
// A path that does not exist yet is still resolved: the deepest existing
// ancestor is resolved and the missing components are re-attached. A file about
// to be created is therefore checked against the real directory its parent
// points at rather than against its lexical path.
func ResolvePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path cannot be empty")
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %q: %w", path, err)
	}
	abs = filepath.Clean(abs)

	// Walk up to the deepest ancestor that exists, resolve it, then re-attach the
	// components that do not exist yet.
	current := abs
	missing := ""
	for {
		resolved, evalErr := filepath.EvalSymlinks(current)
		if evalErr == nil {
			if missing == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, missing), nil
		}
		if !errors.Is(evalErr, fs.ErrNotExist) {
			return "", fmt.Errorf("cannot resolve %q: %w", path, evalErr)
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Reached the filesystem root without finding an existing ancestor;
			// nothing can be resolved, so the cleaned absolute path is the best
			// answer available.
			return abs, nil
		}
		missing = filepath.Join(filepath.Base(current), missing)
		current = parent
	}
}

// PathWithin reports whether path resolves inside root, root itself included.
// Both operands are resolved with ResolvePath before comparison.
func PathWithin(path, root string) (bool, error) {
	resolvedPath, err := ResolvePath(path)
	if err != nil {
		return false, err
	}
	resolvedRoot, err := ResolvePath(root)
	if err != nil {
		return false, err
	}
	if resolvedPath == resolvedRoot {
		return true, nil
	}
	// Not a prefix test: when root is "/" that would require the candidate to
	// start with "//", rejecting every path on the filesystem.
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil {
		return false, nil
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)), nil
}

// PathWithinAny reports whether path resolves inside any of the given roots.
// Empty roots are skipped so callers can pass optional directories unfiltered.
func PathWithinAny(path string, roots ...string) (bool, error) {
	for _, root := range roots {
		if root == "" {
			continue
		}
		within, err := PathWithin(path, root)
		if err != nil {
			return false, err
		}
		if within {
			return true, nil
		}
	}
	return false, nil
}

// EnsurePathWithin returns nil when path resolves inside root, and an error
// naming both operands otherwise. Use it when the caller only needs to reject
// the path, not to know why the check could not be made.
func EnsurePathWithin(path, root string) error {
	within, err := PathWithin(path, root)
	if err != nil {
		return err
	}
	if !within {
		return fmt.Errorf("path %q is outside %q", path, root)
	}
	return nil
}
