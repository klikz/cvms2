package cameras

import (
	"errors"
	"testing"
	"time"

	"cvms/client/internal/config"
)

func newTestManager() *Manager {
	return &Manager{
		online:  map[string]bool{},
		checked: map[string]time.Time{},
		fails:   map[string]int{},
		alive:   map[string]time.Time{},
	}
}

func TestApplyCheckHysteresis(t *testing.T) {
	m := newTestManager()
	var changes []bool
	m.onChange = func(_ string, online bool) { changes = append(changes, online) }
	c := config.Camera{ID: "a", Name: "A"}
	iv := 30 * time.Second
	fail := errors.New("dial timeout")

	m.applyCheck(c, true, nil, false, iv)
	for i := 1; i < offlineAfter; i++ {
		m.applyCheck(c, false, fail, false, iv)
		if !m.Online("a") {
			t.Fatalf("offline after %d failures, want %d", i, offlineAfter)
		}
	}
	m.applyCheck(c, false, fail, false, iv)
	if m.Online("a") {
		t.Fatalf("still online after %d failures", offlineAfter)
	}
	m.applyCheck(c, true, nil, false, iv)
	if !m.Online("a") {
		t.Fatal("not online after first success")
	}
	if len(changes) != 3 {
		t.Fatalf("changes = %v", changes)
	}
}

func TestApplyCheckSuccessResetsCount(t *testing.T) {
	m := newTestManager()
	c := config.Camera{ID: "a"}
	iv := 30 * time.Second
	fail := errors.New("x")
	m.applyCheck(c, true, nil, false, iv)
	for round := 0; round < 3; round++ {
		for i := 1; i < offlineAfter; i++ {
			m.applyCheck(c, false, fail, false, iv)
		}
		m.applyCheck(c, true, nil, false, iv)
	}
	if !m.Online("a") {
		t.Fatal("non-consecutive failures marked camera offline")
	}
}

func TestApplyCheckAliveStream(t *testing.T) {
	m := newTestManager()
	c := config.Camera{ID: "a"}
	iv := 30 * time.Second
	m.MarkAlive("a")
	for i := 0; i < offlineAfter+2; i++ {
		m.applyCheck(c, false, errors.New("x"), false, iv)
	}
	if !m.Online("a") {
		t.Fatal("camera with a live stream went offline")
	}
	m.applyCheck(c, false, errors.New("disabled"), true, iv)
	if m.Online("a") {
		t.Fatal("final failure did not mark offline")
	}
}

func TestApplyCheckUnknownFailsAtOnce(t *testing.T) {
	m := newTestManager()
	var changes []bool
	m.onChange = func(_ string, online bool) { changes = append(changes, online) }
	m.applyCheck(config.Camera{ID: "a"}, false, errors.New("x"), false, time.Second)
	if m.Online("a") || len(changes) != 1 {
		t.Fatalf("unknown camera: online=%v changes=%v", m.Online("a"), changes)
	}
}
