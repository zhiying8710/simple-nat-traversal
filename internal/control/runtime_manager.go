package control

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"simple-nat-traversal/internal/client"
	"simple-nat-traversal/internal/config"
)

type RuntimeStatus struct {
	State      string    `json:"state"`
	ConfigPath string    `json:"config_path,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	StoppedAt  time.Time `json:"stopped_at,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
}

type RuntimeManager struct {
	run func(context.Context, config.ClientConfig) error

	mu         sync.RWMutex
	state      string
	configPath string
	startedAt  time.Time
	stoppedAt  time.Time
	lastError  string
	cancel     context.CancelFunc
	doneCh     chan struct{}
}

func NewRuntimeManager() *RuntimeManager {
	return &RuntimeManager{
		run:   client.Run,
		state: "stopped",
	}
}

func NewRuntimeManagerForTest(run func(context.Context, config.ClientConfig) error) *RuntimeManager {
	return &RuntimeManager{
		run:   run,
		state: "stopped",
	}
}

func (m *RuntimeManager) Start(configPath string) (RuntimeStatus, error) {
	cfg, err := config.LoadClientConfig(configPath)
	if err != nil {
		return RuntimeStatus{}, err
	}
	if _, changed, err := config.EnsureClientIdentity(&cfg); err != nil {
		return RuntimeStatus{}, err
	} else if changed {
		if err := config.SaveClientConfig(configPath, cfg); err != nil {
			return RuntimeStatus{}, err
		}
	}
	configAbs, err := filepath.Abs(configPath)
	if err != nil {
		return RuntimeStatus{}, err
	}

	m.mu.Lock()
	if m.state == "running" || m.state == "starting" || m.state == "stopping" {
		status := m.snapshotLocked()
		m.mu.Unlock()
		return status, errors.New("client runtime is already active")
	}
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	m.state = "starting"
	m.configPath = configAbs
	m.startedAt = time.Now()
	m.stoppedAt = time.Time{}
	m.lastError = ""
	m.cancel = cancel
	m.doneCh = doneCh
	status := m.snapshotLocked()
	m.mu.Unlock()

	go m.runLoop(ctx, cfg, configAbs, doneCh)
	return status, nil
}

func (m *RuntimeManager) Stop(ctx context.Context) (RuntimeStatus, error) {
	m.mu.Lock()
	if m.state == "stopped" {
		status := m.snapshotLocked()
		m.mu.Unlock()
		return status, nil
	}
	cancel := m.cancel
	doneCh := m.doneCh
	m.state = "stopping"
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	if doneCh != nil {
		select {
		case <-doneCh:
		case <-ctx.Done():
			return RuntimeStatus{}, ctx.Err()
		}
	}

	return m.Snapshot(), nil
}

func (m *RuntimeManager) Snapshot() RuntimeStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshotLocked()
}

func (m *RuntimeManager) runLoop(ctx context.Context, cfg config.ClientConfig, configPath string, doneCh chan struct{}) {
	defer close(doneCh)

	m.mu.Lock()
	if m.state == "starting" {
		m.state = "running"
	}
	m.mu.Unlock()

	err := m.run(ctx, cfg)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.cancel = nil
	m.doneCh = nil
	m.stoppedAt = time.Now()
	m.state = "stopped"
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		m.lastError = err.Error()
	} else {
		m.lastError = ""
	}
	m.configPath = configPath
}

func (m *RuntimeManager) snapshotLocked() RuntimeStatus {
	return RuntimeStatus{
		State:      m.state,
		ConfigPath: m.configPath,
		StartedAt:  m.startedAt,
		StoppedAt:  m.stoppedAt,
		LastError:  m.lastError,
	}
}
