//go:build !linux && !windows && !darwin

package netlink

import (
	"errors"
)

func SetupLink(string, string) error {
	// noop
	return nil
}

func LinkByIndex(index int) (*Link, error) {
	return nil, errors.ErrUnsupported
}
