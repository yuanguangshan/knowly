package clipboard

import (
	"testing"
)

func TestHashContent(t *testing.T) {
	m := &Monitor{}

	hash1 := m.hashContent("hello")
	hash2 := m.hashContent("hello")
	hash3 := m.hashContent("world")

	if hash1 != hash2 {
		t.Error("same content should produce same hash")
	}
	if hash1 == hash3 {
		t.Error("different content should produce different hash")
	}
	if len(hash1) != 32 {
		t.Errorf("expected 32-char hex hash, got %d chars", len(hash1))
	}
}

func TestHashImage(t *testing.T) {
	m := &Monitor{}

	data1 := []byte{1, 2, 3}
	data2 := []byte{1, 2, 3}
	data3 := []byte{4, 5, 6}

	hash1 := m.hashImage(data1)
	hash2 := m.hashImage(data2)
	hash3 := m.hashImage(data3)

	if hash1 != hash2 {
		t.Error("same data should produce same hash")
	}
	if hash1 == hash3 {
		t.Error("different data should produce different hash")
	}
}

func TestNewMonitorDefaults(t *testing.T) {
	m := NewMonitor(MonitorConfig{})
	if m.minLength != 100 {
		t.Errorf("expected default minLength 100, got %d", m.minLength)
	}
	if m.maxLength != 1024*1024 {
		t.Errorf("expected default maxLength 1MB, got %d", m.maxLength)
	}
	if m.pollInterval != 500*1e6 {
		t.Errorf("expected default pollInterval 500ms, got %v", m.pollInterval)
	}
}
