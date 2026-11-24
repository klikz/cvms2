//go:build !windows && !linux

package svc

const autostartSupported = false

func mapErr(err error) error                                     { return err }
func canManage() bool                                            { return false }
func queryAutostart(string) (installed, enabled bool, err error) { return false, false, nil }
func setAutostart(string, bool) error                            { return ErrUnsupported }
