package compose

import (
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
)

// resolvePath joins a path written in a Compose file to the directory it
// is relative to, inside the project's file system. Absolute paths and
// paths that climb out of the file system's root are refused: a Compose
// file is untrusted, and must not read files outside the project tree
// (an env_file of /etc/passwd would copy it into a container).
func resolvePath(dir, p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") || strings.Contains(p, `\`) || (len(p) > 1 && p[1] == ':') {
		return "", fmt.Errorf("path %q: absolute paths are not allowed (paths are relative to the Compose file)", p)
	}
	j := path.Clean(path.Join(dir, p))
	if j == ".." || strings.HasPrefix(j, "../") || !fs.ValidPath(j) {
		return "", fmt.Errorf("path %q is outside the project directory", p)
	}
	return j, nil
}

// readFile reads a file of at most max bytes.
func readFile(fsys fs.FS, name string, max int) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil {
		if st.IsDir() {
			return nil, fmt.Errorf("%s is a directory", name)
		}
		if st.Size() > int64(max) {
			return nil, fmt.Errorf("%s is %d bytes, more than the %d allowed", name, st.Size(), max)
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > max {
		return nil, fmt.Errorf("%s is more than the %d bytes allowed", name, max)
	}
	return b, nil
}

func exists(fsys fs.FS, name string) bool {
	st, err := fs.Stat(fsys, name)
	return err == nil && !st.IsDir()
}
