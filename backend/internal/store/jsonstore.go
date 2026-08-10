package store

import (
        "context"
        "encoding/json"
        "os"
        "path/filepath"
        "sync"
)

// JSONStore is a simple key-value settings backend for UmbraForge desktop.
type JSONStore struct {
	mu   sync.Mutex
	path string
	data map[string]string
}

// Dir 返回存储目录（settings.json 所在目录）。
func (s *JSONStore) Dir() string {
	return filepath.Dir(s.path)
}

func NewJSONStore(dir string) (*JSONStore, error) {
        if err := os.MkdirAll(dir, 0o755); err != nil {
                return nil, err
        }
        s := &JSONStore{path: filepath.Join(dir, "settings.json"), data: map[string]string{}}
        _ = s.load()
        return s, nil
}

func (s *JSONStore) load() error {
        b, err := os.ReadFile(s.path)
        if err != nil {
                if os.IsNotExist(err) {
                        return nil
                }
                return err
        }
        return json.Unmarshal(b, &s.data)
}

func (s *JSONStore) save() error {
        b, err := json.MarshalIndent(s.data, "", "  ")
        if err != nil {
                return err
        }
        tmp := s.path + ".tmp"
        if err := os.WriteFile(tmp, b, 0o600); err != nil {
                return err
        }
        return os.Rename(tmp, s.path)
}

func (s *JSONStore) GetValue(_ context.Context, key string) (string, error) {
        s.mu.Lock()
        defer s.mu.Unlock()
        return s.data[key], nil
}

func (s *JSONStore) Set(_ context.Context, key, value string) error {
        s.mu.Lock()
        defer s.mu.Unlock()
        if s.data == nil {
                s.data = map[string]string{}
        }
        s.data[key] = value
        return s.save()
}

func (s *JSONStore) GetAll(_ context.Context) (map[string]string, error) {
        s.mu.Lock()
        defer s.mu.Unlock()
        out := make(map[string]string, len(s.data))
        for k, v := range s.data {
                out[k] = v
        }
        return out, nil
}

func (s *JSONStore) Delete(_ context.Context, key string) error {
        s.mu.Lock()
        defer s.mu.Unlock()
        delete(s.data, key)
        return s.save()
}

func (s *JSONStore) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
        s.mu.Lock()
        defer s.mu.Unlock()
        out := map[string]string{}
        for _, k := range keys {
                if v, ok := s.data[k]; ok {
                        out[k] = v
                }
        }
        return out, nil
}

func (s *JSONStore) SetMultiple(_ context.Context, settings map[string]string) error {
        s.mu.Lock()
        defer s.mu.Unlock()
        if s.data == nil {
                s.data = map[string]string{}
        }
        for k, v := range settings {
                s.data[k] = v
        }
        return s.save()
}

// DataDir returns platform-specific app support path.
func DataDir() (string, error) {
        home, err := os.UserHomeDir()
        if err != nil {
                return "", err
        }
        // Platform-specific under home for simplicity; build tags refine later.
        switch {
        case fileExists(filepath.Join(home, "Library")):
                return filepath.Join(home, "Library", "Application Support", "UmbraForge"), nil
        case os.Getenv("APPDATA") != "":
                return filepath.Join(os.Getenv("APPDATA"), "UmbraForge"), nil
        default:
                return filepath.Join(home, ".config", "umbraforge"), nil
        }
}

func fileExists(p string) bool {
        st, err := os.Stat(p)
        return err == nil && st.IsDir()
}
