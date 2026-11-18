//go:build !linux

package main

import (
	"errors"
	"time"
)

func armKillTimer(time.Duration) error { return errors.New("not on this OS") }
