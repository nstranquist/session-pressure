package sessionpressurecontrol

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nstranquist/session-pressure/pkg/sessionpressurecontrol"
)

type previewStore struct {
	root string
	now  func() time.Time
	mu   sync.Mutex
}

func newPreviewStore(root string, now func() time.Time) (*previewStore, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create control state: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("protect control state: %w", err)
	}
	return &previewStore{root: root, now: now}, nil
}

func (s *previewStore) previewPath(id string) string {
	return filepath.Join(s.root, "previews", id+".json")
}

func (s *previewStore) put(preview sessionpressurecontrol.ActionPreview) error {
	dir := filepath.Dir(s.previewPath(preview.PreviewID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create preview store: %w", err)
	}
	body, err := json.MarshalIndent(preview, "", "  ")
	if err != nil {
		return fmt.Errorf("encode preview: %w", err)
	}
	if len(body)+1 > maxPreviewBytes {
		return fmt.Errorf("preview exceeds the %d-byte store budget", maxPreviewBytes)
	}
	path := s.previewPath(preview.PreviewID)
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, err := os.Lstat(path)
	if err == nil {
		if existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular preview path %s", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect preview path: %w", err)
	} else if err := s.ensureCapacityLocked(); err != nil {
		return err
	}
	return writePrivateFile(path, append(body, '\n'))
}

func (s *previewStore) ensureCapacityLocked() error {
	dir := filepath.Join(s.root, "previews")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read preview store: %w", err)
	}
	now := s.now().UTC()
	count := 0
	for _, entry := range entries {
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		count++
		path := filepath.Join(dir, entry.Name())
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var preview sessionpressurecontrol.ActionPreview
		if json.Unmarshal(body, &preview) != nil {
			continue
		}
		if preview.Status != "pending" || (!preview.ExpiresAt.IsZero() && !now.Before(preview.ExpiresAt)) {
			if removeErr := os.Remove(path); removeErr == nil {
				count--
			}
		}
	}
	if count >= maxPreviewFiles {
		return fmt.Errorf("preview store capacity reached (%d files)", maxPreviewFiles)
	}
	return nil
}

func (s *previewStore) get(id string) (sessionpressurecontrol.ActionPreview, error) {
	body, err := os.ReadFile(s.previewPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return sessionpressurecontrol.ActionPreview{}, errors.New("preview does not exist")
		}
		return sessionpressurecontrol.ActionPreview{}, fmt.Errorf("read preview: %w", err)
	}
	var preview sessionpressurecontrol.ActionPreview
	if err := json.Unmarshal(body, &preview); err != nil {
		return sessionpressurecontrol.ActionPreview{}, fmt.Errorf("decode preview: %w", err)
	}
	return preview, nil
}

func (s *previewStore) appendAudit(record sessionpressurecontrol.AuditRecord) error {
	body, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode audit: %w", err)
	}
	path := filepath.Join(s.root, "audit.jsonl")
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > 4<<20 {
		archive := path + ".1"
		_ = os.Remove(archive)
		if err := os.Rename(path, archive); err != nil {
			return fmt.Errorf("rotate audit: %w", err)
		}
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open audit: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("append audit: %w", err)
	}
	return nil
}

func (s *previewStore) audit(limit int) ([]sessionpressurecontrol.AuditRecord, error) {
	if limit < 1 {
		limit = 1
	}
	path := filepath.Join(s.root, "audit.jsonl")
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []sessionpressurecontrol.AuditRecord{}, nil
		}
		return nil, err
	}
	defer file.Close()
	rows := make([]sessionpressurecontrol.AuditRecord, 0, limit)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), maxAuditBytes)
	for scanner.Scan() {
		var row sessionpressurecontrol.AuditRecord
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			continue
		}
		rows = append(rows, row)
		if len(rows) > limit {
			rows = rows[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

func writePrivateFile(path string, body []byte) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular private file %s", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func stateHash(root string) (string, error) {
	hash := sha256.New()
	paths := make([]string, 0, 32)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "api" || strings.HasPrefix(relative, "api"+string(filepath.Separator)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 1<<20 {
			return nil
		}
		paths = append(paths, relative)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk pressure state: %w", err)
	}
	sort.Strings(paths)
	for _, relative := range paths {
		body, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte(relative))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(body)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
