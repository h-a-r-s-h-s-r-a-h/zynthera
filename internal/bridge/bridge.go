// Package bridge is the file transfer service that runs inside every guest
// as the user's own account (spec section 14). QEMU's display has no file
// transfer, so the host agent talks to this service instead. It uses only
// the standard library, so the binary is small and has nothing to patch.
package bridge

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Server serves one user's home directory.
type Server struct {
	home      string // the home directory with symlinks resolved
	tokenHash [32]byte
	maxUpload int64 // bytes; 0 means no limit of its own
}

// New returns a server for the given home directory and token.
func New(home, token string, maxUploadBytes int64) (*Server, error) {
	if token == "" {
		return nil, errors.New("bridge: a token is required")
	}
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		return nil, fmt.Errorf("bridge: home directory: %w", err)
	}
	if !filepath.IsAbs(resolved) {
		return nil, errors.New("bridge: home directory must be an absolute path")
	}
	return &Server{home: resolved, tokenHash: sha256.Sum256([]byte(token)), maxUpload: maxUploadBytes}, nil
}

// Handler returns the HTTP handler. Every request needs the bearer token.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/list", s.handleList)
	mux.HandleFunc("GET /v1/download", s.handleDownload)
	mux.HandleFunc("PUT /v1/upload", s.handleUpload)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		got := sha256.Sum256([]byte(token))
		if !ok || subtle.ConstantTimeCompare(got[:], s.tokenHash[:]) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Missing or wrong token.")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "details": map[string]any{}}})
}

// errOutside means a path leads out of the home directory.
var errOutside = errors.New("bridge: path is outside the home directory")

// resolve turns a path relative to the home directory into a real path, and
// refuses it unless the result, with every symlink followed, is still inside
// the home directory. The path must exist.
func (s *Server) resolve(rel string) (string, error) {
	if strings.ContainsRune(rel, 0) || filepath.IsAbs(rel) || strings.HasPrefix(rel, `\`) {
		return "", errOutside
	}
	// Clean removes ".." where it can; whatever is left is caught by the
	// check on the resolved path below.
	joined := filepath.Join(s.home, filepath.Clean("/"+rel))
	real, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", err
	}
	if !s.inside(real) {
		return "", errOutside
	}
	return real, nil
}

func (s *Server) inside(real string) bool {
	return real == s.home || strings.HasPrefix(real, s.home+string(filepath.Separator))
}

func (s *Server) pathError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errOutside):
		writeError(w, http.StatusForbidden, "forbidden", "That path is outside your home folder.")
	case errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, "not_found", "No such file or folder.")
	case errors.Is(err, fs.ErrPermission):
		writeError(w, http.StatusForbidden, "forbidden", "You do not have permission to read that.")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "The file operation failed.")
	}
}

// Entry is one item of a directory listing.
type Entry struct {
	Name     string    `json:"name"`
	Type     string    `json:"type"` // "file" | "dir"
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// GET /v1/list?path=<relative dir>[&hidden=1]
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	dir, err := s.resolve(r.URL.Query().Get("path"))
	if err != nil {
		s.pathError(w, err)
		return
	}
	items, err := os.ReadDir(dir)
	if err != nil {
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) && errors.Is(pathErr.Err, errNotDir()) {
			writeError(w, http.StatusBadRequest, "validation_failed", "That is a file, not a folder.")
			return
		}
		s.pathError(w, err)
		return
	}
	showHidden := r.URL.Query().Get("hidden") == "1"
	out := []Entry{}
	for _, item := range items {
		name := item.Name()
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		// Stat follows symlinks; a link that leads out of the home
		// directory, or nowhere, is simply not listed.
		real, err := filepath.EvalSymlinks(filepath.Join(dir, name))
		if err != nil || !s.inside(real) {
			continue
		}
		info, err := os.Stat(real)
		if err != nil {
			continue
		}
		switch {
		case info.IsDir():
			out = append(out, Entry{Name: name, Type: "dir", Modified: info.ModTime().UTC()})
		case info.Mode().IsRegular():
			out = append(out, Entry{Name: name, Type: "file", Size: info.Size(), Modified: info.ModTime().UTC()})
		}
	}
	// Folders first, then by name.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type == "dir"
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	writeJSON(w, http.StatusOK, map[string]any{"entries": out})
}

// GET /v1/download?path=<relative file>
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	path, err := s.resolve(r.URL.Query().Get("path"))
	if err != nil {
		s.pathError(w, err)
		return
	}
	f, err := os.Open(path) //nolint:gosec // the path was confined to the home directory by resolve
	if err != nil {
		s.pathError(w, err)
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		s.pathError(w, err)
		return
	}
	if !info.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, "validation_failed", "Only files can be downloaded.")
		return
	}
	h := w.Header()
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(path)}))
	// Never let a browser guess a type and run or render a downloaded file.
	h.Set("Content-Type", "application/octet-stream")
	h.Set("X-Content-Type-Options", "nosniff")
	// ServeContent streams the file and supports resuming a download.
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// uploadName reduces what the client sent to a single safe file name.
func uploadName(raw string) (string, bool) {
	name := filepath.Base(strings.ReplaceAll(raw, `\`, "/"))
	if name == "" || name == "." || name == ".." || name == "/" ||
		strings.HasPrefix(name, ".") || strings.ContainsRune(name, 0) || len(name) > 255 {
		return "", false
	}
	return name, true
}

// freeName reserves a name that is not taken yet in dir by creating the
// empty file: "report.pdf", then "report (1).pdf", "report (2).pdf", …
func freeName(dir, name string) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i < 10000; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		f, err := os.OpenFile(filepath.Join(dir, candidate), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644) //nolint:gosec // dir is the user's own Uploads folder; the file is theirs
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_ = f.Close()
		return candidate, nil
	}
	return "", errors.New("bridge: too many files with the same name")
}

// PUT /v1/upload?name=<file name>: saves the body to ~/Uploads/<name>.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	name, ok := uploadName(r.URL.Query().Get("name"))
	if !ok {
		writeError(w, http.StatusBadRequest, "validation_failed", "That file name cannot be used.")
		return
	}
	uploads := filepath.Join(s.home, "Uploads")
	if err := os.MkdirAll(uploads, 0o755); err != nil { //nolint:gosec // an ordinary folder in the user's home
		s.pathError(w, err)
		return
	}
	// "Uploads" could be a symlink the user made to somewhere else.
	dir, err := filepath.EvalSymlinks(uploads)
	if err != nil || !s.inside(dir) {
		s.pathError(w, errOutside)
		return
	}

	// Write to a temporary file in the same folder and rename it when the
	// upload is complete, so a failed upload leaves nothing half-written.
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		s.pathError(w, err)
		return
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()

	var body io.Reader = r.Body
	if s.maxUpload > 0 {
		body = http.MaxBytesReader(w, r.Body, s.maxUpload)
	}
	size, err := io.Copy(tmp, body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "The file is larger than the upload limit.")
			return
		}
		writeError(w, http.StatusBadRequest, "upload_failed", "The upload did not complete.")
		return
	}
	if err := tmp.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "The file could not be saved.")
		return
	}
	final, err := freeName(dir, name)
	if err != nil {
		s.pathError(w, err)
		return
	}
	// "final" is a single path component (uploadName strips directories)
	// and "dir" was checked to be inside the home directory.
	target := filepath.Join(dir, final)
	if err := os.Rename(tmpPath, target); err != nil { //nolint:gosec // G703: see comment above
		_ = os.Remove(target) //nolint:gosec // G703: see comment above
		s.pathError(w, err)
		return
	}
	keep = true
	_ = os.Chmod(target, 0o644) //nolint:gosec // the user's own file
	writeJSON(w, http.StatusCreated, map[string]any{"name": final, "path": "Uploads/" + final, "size": size})
}
