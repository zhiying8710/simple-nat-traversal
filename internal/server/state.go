package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"simple-nat-traversal/internal/secure"
)

type persistedState struct {
	DeviceOwners map[string]string `json:"device_owners,omitempty"`
}

func (s *Server) loadState() error {
	if strings.TrimSpace(s.cfg.StatePath) == "" {
		return nil
	}

	raw, err := os.ReadFile(s.cfg.StatePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read %s: %w", s.cfg.StatePath, err)
	}

	var state persistedState
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("decode %s: %w", s.cfg.StatePath, err)
	}
	for name, encoded := range state.DeviceOwners {
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return fmt.Errorf("decode device owner %q: %w", name, err)
		}
		publicKey, err := secure.ParseIdentityPublicKey(decoded)
		if err != nil {
			return fmt.Errorf("parse device owner %q: %w", name, err)
		}
		s.deviceOwners[name] = slices.Clone(publicKey)
	}
	return nil
}

func (s *Server) saveStateLocked() error {
	if strings.TrimSpace(s.cfg.StatePath) == "" {
		return nil
	}

	state := persistedState{
		DeviceOwners: make(map[string]string, len(s.deviceOwners)),
	}
	for name, publicKey := range s.deviceOwners {
		state.DeviceOwners[name] = base64.RawURLEncoding.EncodeToString(publicKey)
	}

	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(s.cfg.StatePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.cfg.StatePath, raw, 0o600)
}

func (s *Server) canClaimDeviceNameLocked(name string, identityPublic []byte) bool {
	owner, ok := s.deviceOwners[name]
	return !ok || slices.Equal(owner, identityPublic)
}

func (s *Server) claimDeviceNameLocked(name string, identityPublic []byte) error {
	if existing, ok := s.deviceOwners[name]; ok {
		if slices.Equal(existing, identityPublic) {
			return nil
		}
		return errors.New("device name is already owned by another identity")
	}
	s.deviceOwners[name] = slices.Clone(identityPublic)
	if err := s.saveStateLocked(); err != nil {
		delete(s.deviceOwners, name)
		return err
	}
	return nil
}
