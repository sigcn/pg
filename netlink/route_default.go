//go:build !linux && !windows && !darwin

package netlink

import (
	"context"
	"errors"
	"net"
)

func RouteSubscribe(ctx context.Context, ch chan<- RouteUpdate) error {
	return errors.ErrUnsupported
}
func AddRoute(string, *net.IPNet, net.IP) error {
	// noop
	return errors.ErrUnsupported
}

func DelRoute(string, *net.IPNet, net.IP) error {
	// noop
	return errors.ErrUnsupported
}
