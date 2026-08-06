// Package permissions defines runtime access modes and session approval state.
package permissions

import (
	"fmt"
	"strings"
	"sync"
)

// Mode controls sandboxing and automatic approvals.
type Mode string

const (
	// ModeDefault keeps workspace and network sandboxing enabled.
	ModeDefault Mode = "default"
	// ModeFullAccess allows host, network, and outside-workspace access.
	ModeFullAccess Mode = "full-access"
	// ModeYOLO also automatically approves untrusted external tools.
	ModeYOLO Mode = "yolo"
)

// ParseMode parses a permission mode. An empty value selects the default sandbox.
func ParseMode(value string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "default", "sandbox":
		return ModeDefault, nil
	case "full-access", "full_access", "full":
		return ModeFullAccess, nil
	case "yolo":
		return ModeYOLO, nil
	default:
		return "", fmt.Errorf("无效权限模式 %q，可选值：default、full-access、yolo", value)
	}
}

// Label returns the user-facing mode name.
func (m Mode) Label() string {
	switch m {
	case ModeFullAccess:
		return "完全访问"
	case ModeYOLO:
		return "YOLO"
	default:
		return "默认沙箱"
	}
}

// HasFullAccess reports whether filesystem and network sandboxing is disabled.
func (m Mode) HasFullAccess() bool {
	return m == ModeFullAccess || m == ModeYOLO
}

// Policy stores the current permission mode and session-scoped approvals.
type Policy struct {
	mu      sync.RWMutex
	mode    Mode
	allowed map[string]struct{}
}

// NewPolicy creates a permission policy for one process session.
func NewPolicy(mode Mode) *Policy {
	return &Policy{mode: mode, allowed: make(map[string]struct{})}
}

// Mode returns the current mode.
func (p *Policy) Mode() Mode {
	if p == nil {
		return ModeDefault
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mode
}

// SetMode switches the current mode without persisting it.
func (p *Policy) SetMode(mode Mode) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.mode = mode
	p.mu.Unlock()
}

// Allow remembers an approval scope for the current process session.
func (p *Policy) Allow(scope string) {
	if p == nil || strings.TrimSpace(scope) == "" {
		return
	}
	p.mu.Lock()
	p.allowed[scope] = struct{}{}
	p.mu.Unlock()
}

// Allows reports whether an approval scope was remembered for this session.
func (p *Policy) Allows(scope string) bool {
	if p == nil || strings.TrimSpace(scope) == "" {
		return false
	}
	p.mu.RLock()
	_, ok := p.allowed[scope]
	p.mu.RUnlock()
	return ok
}
