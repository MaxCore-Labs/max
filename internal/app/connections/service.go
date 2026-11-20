package connections

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"max/internal/app"
)

// ConnTool classifies one tool exposed by an external MCP connection.
type ConnTool struct {
	Name        string `json:"name"`
	Write       bool   `json:"write"`
	TargetField string `json:"target_field,omitempty"`
}

// Connection describes a configured external MCP server whose Config bytes are encrypted at rest.
type Connection struct {
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Config     []byte     `json:"-"` // encrypted ciphertext in DB; never serialized in API responses
	Configured bool       `json:"configured"`
	Enabled    bool       `json:"enabled"`
	Tools      []ConnTool `json:"tools"`
}

// UpsertInput carries connection metadata and optional plaintext Config JSON to encrypt.
type UpsertInput struct {
	Name    string
	Kind    string
	Config  []byte // plaintext JSON; if empty on update, existing ciphertext is preserved
	Tools   []ConnTool
	Enabled bool
}

// Service manages external MCP connection registrations encrypted with <DataDir>/.key (0600).
type Service struct {
	store app.Store
	clock app.Clock
	aead  cipher.AEAD
}

// NewService loads or creates <dataDir>/.key (0600) and constructs a connections Service.
func NewService(store app.Store, dataDir string, clock app.Clock) (*Service, error) {
	if clock == nil {
		clock = app.SystemClock{}
	}
	key, err := loadOrCreateKey(dataDir)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aes gcm: %w", err)
	}
	return &Service{store: store, clock: clock, aead: aead}, nil
}

func loadOrCreateKey(dataDir string) ([]byte, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir data dir: %w", err)
	}
	keyPath := filepath.Join(dataDir, ".key")
	if existing, err := os.ReadFile(keyPath); err == nil && len(existing) == 32 {
		_ = os.Chmod(keyPath, 0o600)
		return existing, nil
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate .key: %w", err)
	}
	f, err := os.OpenFile(keyPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open .key: %w", err)
	}
	_, wErr := f.Write(key)
	cErr := f.Close()
	if wErr != nil {
		return nil, wErr
	}
	if cErr != nil {
		return nil, cErr
	}
	_ = os.Chmod(keyPath, 0o600)
	return key, nil
}

// EncryptBytes encrypts plaintext using AES-256-GCM with a random nonce.
func (s *Service) EncryptBytes(plain []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, plain, nil), nil
}

// DecryptBytes decrypts AES-256-GCM ciphertext created by EncryptBytes.
func (s *Service) DecryptBytes(ct []byte) ([]byte, error) {
	ns := s.aead.NonceSize()
	if len(ct) < ns {
		return nil, errors.New("ciphertext too short")
	}
	return s.aead.Open(nil, ct[:ns], ct[ns:], nil)
}

// Upsert encrypts in.Config (when provided) and persists the connection definition.
func (s *Service) Upsert(ctx context.Context, in UpsertInput) error {
	name := strings.TrimSpace(in.Name)
	kind := strings.TrimSpace(in.Kind)
	if name == "" || kind == "" {
		return errors.New("connection name and kind are required")
	}
	var ct []byte
	if len(in.Config) > 0 {
		var err error
		ct, err = s.EncryptBytes(in.Config)
		if err != nil {
			return err
		}
	} else {
		if _, ok, _ := s.store.Read().GetConnection(ctx, name); !ok {
			ct, _ = s.EncryptBytes([]byte(`{}`))
		}
	}
	toolsBytes, _ := json.Marshal(in.Tools)
	if len(in.Tools) == 0 {
		toolsBytes = []byte("[]")
	}
	nowMs := s.clock.Now().UnixMilli()
	return s.store.WithTx(ctx, func(tx app.Tx) error {
		return tx.UpsertConnection(ctx, app.ConnectionRecord{
			Name:             name,
			Kind:             kind,
			ConfigCiphertext: ct,
			ToolsJSON:        string(toolsBytes),
			Enabled:          in.Enabled,
			CreatedAt:        nowMs,
			UpdatedAt:        nowMs,
		})
	})
}

// Get returns connection metadata without exposing decrypted secrets.
func (s *Service) Get(ctx context.Context, name string) (Connection, bool, error) {
	rec, ok, err := s.store.Read().GetConnection(ctx, name)
	if err != nil || !ok {
		return Connection{}, ok, err
	}
	return toConnection(rec, s.hasNonEmptySecrets(rec.ConfigCiphertext)), true, nil
}

// List returns all registered connections with write-only Configured flags (never returning secrets).
func (s *Service) List(ctx context.Context) ([]Connection, error) {
	rows, err := s.store.Read().ListConnections(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Connection, 0, len(rows))
	for _, r := range rows {
		out = append(out, toConnection(r, s.hasNonEmptySecrets(r.ConfigCiphertext)))
	}
	return out, nil
}

// Delete removes a connection by name.
func (s *Service) Delete(ctx context.Context, name string) error {
	return s.store.WithTx(ctx, func(tx app.Tx) error {
		return tx.DeleteConnection(ctx, name)
	})
}

// DecryptConfig decrypts and returns the raw config JSON for outbound transport invocation only.
func (s *Service) DecryptConfig(ctx context.Context, name string) ([]byte, error) {
	rec, ok, err := s.store.Read().GetConnection(ctx, name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("connection %q not found", name)
	}
	return s.DecryptBytes(rec.ConfigCiphertext)
}

// SecretValues returns non-URL secret string values from decrypted config so Gateway scrubs accidental echoes.
func (s *Service) SecretValues(plain []byte) []string {
	var m map[string]any
	if json.Unmarshal(plain, &m) != nil {
		return nil
	}
	var vals []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		case string:
			t := strings.TrimSpace(x)
			if len(t) >= 8 && !strings.HasPrefix(t, "http://") && !strings.HasPrefix(t, "https://") {
				vals = append(vals, t)
			}
		}
	}
	walk(m)
	return vals
}

func (s *Service) hasNonEmptySecrets(ct []byte) bool {
	plain, err := s.DecryptBytes(ct)
	if err != nil {
		return false
	}
	trimmed := strings.TrimSpace(string(plain))
	return trimmed != "" && trimmed != "{}"
}

func toConnection(r app.ConnectionRecord, configured bool) Connection {
	var tools []ConnTool
	_ = json.Unmarshal([]byte(r.ToolsJSON), &tools)
	return Connection{
		Name:       r.Name,
		Kind:       r.Kind,
		Config:     r.ConfigCiphertext,
		Configured: configured,
		Enabled:    r.Enabled,
		Tools:      tools,
	}
}
