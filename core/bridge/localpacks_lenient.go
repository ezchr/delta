package bridge

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sandertv/gophertunnel/minecraft/resource"
)

// readPackLenient reads a pack folder the strict reader refuses (BDS accepts a manifest that
// starts with a UTF-8 byte order mark, the reader does not): the folder is zipped in memory with
// the marks dropped from its JSON files.
func readPackLenient(dir string) (*resource.Pack, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.EqualFold(filepath.Ext(path), ".json") {
			b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
		}
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return resource.Read(bytes.NewReader(buf.Bytes()))
}
