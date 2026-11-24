//go:build windows

package svc

import (
	"errors"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

const autostartSupported = true

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return ErrNeedAdmin
	}
	return err
}

// canManage checks for rights to create/configure services.
func canManage() bool {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CREATE_SERVICE)
	if err != nil {
		return false
	}
	windows.CloseServiceHandle(h)
	return true
}

func queryAutostart(name string) (installed, enabled bool, err error) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false, false, mapErr(err)
	}
	defer windows.CloseServiceHandle(scm)
	n, _ := windows.UTF16PtrFromString(name)
	h, err := windows.OpenService(scm, n, windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return false, false, nil
		}
		return false, false, mapErr(err)
	}
	s := &mgr.Service{Name: name, Handle: h}
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return true, false, mapErr(err)
	}
	return true, c.StartType == mgr.StartAutomatic, nil
}

func setAutostart(name string, on bool) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return err
	}
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return err
	}
	c.StartType = mgr.StartManual
	if on {
		c.StartType = mgr.StartAutomatic
		c.DelayedAutoStart = true // start after the network is up
	} else {
		c.DelayedAutoStart = false
	}
	return s.UpdateConfig(c)
}
