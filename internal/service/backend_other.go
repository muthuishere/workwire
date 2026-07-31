//go:build !darwin && !linux && !windows

package service

import "fmt"

const defaultLabel = "workwire"

// New returns a backend that refuses loudly rather than half-installing on an
// OS whose service manager we do not speak.
func New() Backend { return unsupported{} }

type unsupported struct{}

func (unsupported) Name() string { return "unsupported" }

func (unsupported) Install(Spec) error {
	return fmt.Errorf("no service backend for this OS: run `workwire serve` under your own supervisor")
}

func (unsupported) Uninstall(Spec) error {
	return fmt.Errorf("no service backend for this OS: nothing to uninstall")
}

func (unsupported) Status(Spec) (string, error) {
	return "", fmt.Errorf("no service backend for this OS")
}

func (unsupported) Hint() string { return "" }
