package engine

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"presto/internal/config"
)

const Protocol = 1
const MaxArchiveBytes int64 = 512 << 20
const maxExpandedBytes uint64 = 2 << 30
const requestName = ".presto-request.json"

type request struct {
	Protocol int `json:"protocol"`
	Snapshot
}

// WriteArchive writes reproducible ZIP entries; deployment metadata is omitted from releases.
func WriteArchive(ctx context.Context, snapshot *Snapshot, writer io.Writer, deployment bool) error {
	archive := zip.NewWriter(writer)
	if deployment {
		data, err := json.Marshal(request{Protocol, *snapshot})
		if err != nil {
			return err
		}
		entry, err := archive.CreateHeader(header(requestName, 0644))
		if err != nil {
			return err
		}
		if _, err := entry.Write(data); err != nil {
			return err
		}
	}
	for _, file := range snapshot.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, err := archive.CreateHeader(header(snapshot.Name+"/"+file.Path, fs.FileMode(file.Mode)))
		if err != nil {
			return err
		}
		in, err := os.Open(filepath.Join(snapshot.Root, filepath.FromSlash(file.Path)))
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(entry, in)
		closeErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return archive.Close()
}

func header(name string, mode fs.FileMode) *zip.FileHeader {
	h := &zip.FileHeader{Name: name, Method: zip.Deflate}
	h.SetMode(mode)
	h.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
	return h
}

// Receive validates an entire archive into private temporary storage before touching a shop.
func Receive(ctx context.Context, reader io.Reader, shopRoot string, dryRun bool) (Result, error) {
	var result Result
	workspace, err := os.MkdirTemp("", "presto-receive-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(workspace)
	file, err := os.Create(filepath.Join(workspace, "payload.zip"))
	if err != nil {
		return result, err
	}
	n, copyErr := io.Copy(file, io.LimitReader(reader, MaxArchiveBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return result, copyErr
	}
	if closeErr != nil {
		return result, closeErr
	}
	if n > MaxArchiveBytes {
		return result, fmt.Errorf("deployment archive exceeds %d bytes", MaxArchiveBytes)
	}
	archive, err := zip.OpenReader(file.Name())
	if err != nil {
		return result, fmt.Errorf("open deployment ZIP: %w", err)
	}
	defer archive.Close()
	if len(archive.File) > 100001 {
		return result, fmt.Errorf("too many archive entries")
	}
	entries := map[string]*zip.File{}
	var expanded uint64
	for _, entry := range archive.File {
		if !validPath(entry.Name) || entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() {
			return result, fmt.Errorf("invalid archive entry %q", entry.Name)
		}
		if _, exists := entries[entry.Name]; exists {
			return result, fmt.Errorf("duplicate archive entry %q", entry.Name)
		}
		if entry.UncompressedSize64 > maxExpandedBytes-expanded {
			return result, fmt.Errorf("deployment expands beyond 2 GiB")
		}
		expanded += entry.UncompressedSize64
		entries[entry.Name] = entry
	}
	metadata, exists := entries[requestName]
	if !exists || metadata.UncompressedSize64 > 16<<20 {
		return result, fmt.Errorf("missing or oversized deployment metadata")
	}
	in, err := metadata.Open()
	if err != nil {
		return result, err
	}
	decoder := json.NewDecoder(io.LimitReader(in, 16<<20+1))
	decoder.DisallowUnknownFields()
	var req request
	err = decoder.Decode(&req)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = fmt.Errorf("trailing deployment metadata")
		}
	}
	if closeErr := in.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return result, fmt.Errorf("deployment metadata: %w", err)
	}
	if req.Protocol != Protocol {
		return result, fmt.Errorf("unsupported deployment protocol %d (expected %d)", req.Protocol, Protocol)
	}
	if !ValidModule(req.Name) || len(req.Files) == 0 {
		return result, fmt.Errorf("invalid module metadata")
	}
	for _, patterns := range [][]string{req.Rules.Exclude, req.Rules.Include, req.Rules.Preserve} {
		for _, pattern := range patterns {
			if err := config.ValidatePattern(pattern); err != nil {
				return result, err
			}
		}
	}
	if len(entries) != len(req.Files)+1 {
		return result, fmt.Errorf("archive and file manifest disagree")
	}
	root := filepath.Join(workspace, "module")
	if err := os.Mkdir(root, 0700); err != nil {
		return result, err
	}
	seen := map[string]bool{}
	casePaths := map[string]string{}
	foundMain := false
	for _, file := range req.Files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !validPath(file.Path) || seen[file.Path] || !req.Rules.Selected(file.Path) || (file.Mode != 0644 && file.Mode != 0755) {
			return result, fmt.Errorf("invalid file in manifest: %q", file.Path)
		}
		seen[file.Path] = true
		for name := file.Path; name != "."; name = path.Dir(name) {
			key := strings.ToLower(name)
			if previous, exists := casePaths[key]; exists && previous != name {
				return result, fmt.Errorf("case-colliding archive paths %q and %q", previous, name)
			}
			casePaths[key] = name
		}
		entry, exists := entries[req.Name+"/"+file.Path]
		if !exists || file.Size < 0 || uint64(file.Size) != entry.UncompressedSize64 {
			return result, fmt.Errorf("archive size mismatch for %q", file.Path)
		}
		if file.Path == req.Name+".php" {
			foundMain = true
		}
		in, err := entry.Open()
		if err != nil {
			return result, err
		}
		destination := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			in.Close()
			return result, err
		}
		out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(file.Mode))
		if err != nil {
			in.Close()
			return result, err
		}
		_, copyErr := io.Copy(out, io.LimitReader(in, int64(entry.UncompressedSize64)+1))
		inErr, outErr := in.Close(), out.Close()
		if copyErr != nil {
			return result, copyErr
		}
		if inErr != nil {
			return result, inErr
		}
		if outErr != nil {
			return result, outErr
		}
		hash, size, err := hashFile(destination)
		if err != nil || hash != file.SHA256 || size != file.Size {
			return result, fmt.Errorf("checksum mismatch for %q", file.Path)
		}
	}
	if !foundMain {
		return result, fmt.Errorf("module entry point missing from manifest")
	}
	req.Root = root
	return Synchronize(ctx, &req.Snapshot, shopRoot, dryRun)
}

func validPath(name string) bool {
	if !fs.ValidPath(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\:\x00\r\n") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if strings.ContainsAny(part, `<>"|?*`) {
			return false
		}
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
			return false
		}
	}
	return name != "."
}
