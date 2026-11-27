// Package archive reads and writes ZIP bundles of cookie logs.
//
// Extract never writes to disk, so there is no path-traversal risk, but a
// malicious archive can still exhaust memory. Every read is bounded by file
// count, per-file size and total uncompressed size.
package archive

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Limits bounds decompression.
type Limits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

// DefaultLimits is sized for a 20 MB Telegram upload that expands roughly 6x.
func DefaultLimits() Limits {
	return Limits{
		MaxFiles:      500,
		MaxFileBytes:  16 << 20,
		MaxTotalBytes: 128 << 20,
	}
}

// File is one extracted text file.
type File struct {
	Name string
	Data []byte
}

// Result carries extracted files plus human-readable notes about anything
// skipped, so the bot can explain what it ignored instead of silently losing
// accounts.
type Result struct {
	Files   []File
	Skipped []string
}

// LooksZipped reports whether data starts with the ZIP local-file signature.
func LooksZipped(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && data[2] == 3 && data[3] == 4
}

// Extract reads every text entry from a ZIP held in memory.
func Extract(data []byte, lim Limits) (Result, error) {
	var res Result

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return res, fmt.Errorf("not a zip: %w", err)
	}

	var total int64

	for _, f := range zr.File {
		name := f.Name

		if f.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
			continue
		}

		clean := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(name, "/")))
		if strings.HasPrefix(clean, "..") || strings.Contains(clean, "__MACOSX") {
			continue
		}

		if len(res.Files) >= lim.MaxFiles {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s — file count limit reached", clean))
			continue
		}
		if int64(f.UncompressedSize64) > lim.MaxFileBytes {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s — larger than %d MB", clean, lim.MaxFileBytes>>20))
			continue
		}
		if total+int64(f.UncompressedSize64) > lim.MaxTotalBytes {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s — archive exceeds %d MB uncompressed", clean, lim.MaxTotalBytes>>20))
			continue
		}

		rc, err := f.Open()
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s — %v", clean, err))
			continue
		}

		content, err := io.ReadAll(io.LimitReader(rc, lim.MaxFileBytes))
		_ = rc.Close()
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s — %v", clean, err))
			continue
		}

		total += int64(len(content))

		if isBinary(content) {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s — binary, skipped", clean))
			continue
		}
		if len(bytes.TrimSpace(content)) == 0 {
			continue
		}

		res.Files = append(res.Files, File{Name: clean, Data: content})
	}

	return res, nil
}

// Build writes a ZIP from the given files.
func Build(files []File) ([]byte, error) {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.Name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.Data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func isBinary(b []byte) bool {
	n := len(b)
	if n > 512 {
		n = 512
	}
	for i := 0; i < n; i++ {
		if b[i] == 0 {
			return true
		}
	}
	return false
}
