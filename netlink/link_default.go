//go:build !linux && !windows && !darwin

package netlink

import (
	"errors"
	"net"
)

func SetupLink(string, string) error {
	// noop
	return nil
}

func AddRoute(string, *net.IPNet, net.IP) error {
	// noop
	return nil
}

func DelRoute(string, *net.IPNet, net.IP) error {
	// noop
	return nil
}

func LinkByIndex(index int) (*Link, error) {
	return nil, errors.ErrUnsupported
}
