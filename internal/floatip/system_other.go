//go:build !linux

package floatip

import "errors"

// System is not available off Linux: a floating IP is a Linux-only feature.
type System struct{}

var errNotLinux = errors.New("a floating IP needs Linux")

func (System) Add(Config) error      { return errNotLinux }
func (System) Del(Config) error      { return errNotLinux }
func (System) Announce(Config) error { return errNotLinux }
