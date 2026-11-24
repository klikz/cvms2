package svc

import (
	"errors"
	"runtime"

	"github.com/kardianos/service"

	"cvms/shared/appdir"
)

// Autostart errors.
var (
	ErrNeedAdmin   = errors.New("administrator (root) rights are required")
	ErrDevMode     = errors.New("not available when running with go run")
	ErrUnsupported = errors.New("autostart is not supported on this OS")
)

// AutoStatus is the autostart state shown in the UI.
type AutoStatus struct {
	Supported bool   `json:"supported"`
	Dev       bool   `json:"dev"`
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	AsService bool   `json:"as_service"` // this process runs as the service
	NeedAdmin bool   `json:"need_admin"`
	Error     string `json:"error,omitempty"`
	OS        string `json:"os"`
}

type noop struct{}

func (noop) Start(service.Service) error { return nil }
func (noop) Stop(service.Service) error  { return nil }

// AutoStatusOf reads the autostart state of the service.
func AutoStatusOf(spec Spec) AutoStatus {
	st := AutoStatus{Supported: autostartSupported, Dev: appdir.IsDev(), AsService: !service.Interactive(), OS: runtime.GOOS}
	if !st.Supported {
		return st
	}
	installed, enabled, err := queryAutostart(spec.Name)
	st.Installed, st.Enabled = installed, enabled
	if err != nil {
		if errors.Is(err, ErrNeedAdmin) {
			st.NeedAdmin = true
		} else {
			st.Error = err.Error()
		}
	}
	if !st.NeedAdmin && !canManage() {
		st.NeedAdmin = true
	}
	return st
}

// EnsureAutostart is called at startup: when want is true and the service
// is missing or set to manual start, it installs/enables it. It never
// disables anything (a user choice of "off" is applied through the UI).
func EnsureAutostart(spec Spec, want bool) (AutoStatus, error) {
	st := AutoStatusOf(spec)
	if !want || !st.Supported || st.Dev || (st.Installed && st.Enabled) {
		return st, nil
	}
	if st.NeedAdmin {
		return st, ErrNeedAdmin
	}
	err := SetAutostart(spec, true)
	return AutoStatusOf(spec), err
}

// SetAutostart makes the app start with the OS (installing the service on
// first use) or switches the service to manual start.
func SetAutostart(spec Spec, enable bool) error {
	if !autostartSupported {
		return ErrUnsupported
	}
	if appdir.IsDev() {
		return ErrDevMode
	}
	if !canManage() {
		return ErrNeedAdmin
	}
	installed, _, err := queryAutostart(spec.Name)
	if err != nil && !errors.Is(err, ErrNeedAdmin) {
		return err
	}
	if !installed {
		if !enable {
			return nil
		}
		s, err := service.New(noop{}, spec.config())
		if err != nil {
			return err
		}
		if err := s.Install(); err != nil {
			return mapErr(err)
		}
	}
	return mapErr(setAutostart(spec.Name, enable))
}
