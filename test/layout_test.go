package peruri_test

import (
	"bytes"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceLayout(t *testing.T) {
	root, err := filepath.Abs("..")
	check(t, err)
	if _, err = os.Stat(filepath.Join(root, "internal", "repositories", "memory")); !os.IsNotExist(err) {
		t.Fatal("alternate storage directory still exists", err)
	}
	hashes := make(map[[32]byte]string)
	sources, tests := 0, 0
	for _, directory := range []string{"docs", "cmd", "internal", "db", "test"} {
		err = filepath.WalkDir(filepath.Join(root, directory), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if strings.HasSuffix(path, "_test.go") {
				tests++
				if filepath.Dir(relative) != "test" {
					t.Errorf("test outside test/: %s", relative)
				}
			}
			if extension := filepath.Ext(path); extension != ".go" && extension != ".sql" {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(content, []byte("peruri/internal/repositories/memory")) && !strings.HasSuffix(path, "_test.go") {
				t.Errorf("obsolete storage import: %s", relative)
			}
			normalized := bytes.TrimSpace(bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n")))
			hash := sha256.Sum256(normalized)
			if previous, exists := hashes[hash]; exists {
				t.Errorf("duplicate source files: %s and %s", previous, relative)
			}
			hashes[hash] = relative
			sources++
			return nil
		})
		check(t, err)
	}
	if tests == 0 {
		t.Fatal("no tests discovered")
	}
	t.Logf("checked %d source files; all %d test files are in test/", sources, tests)
}
