//go:build !linux && !windows && !darwin

package netlink

import (
	"context"
	"errors"
)

func AddrSubscribe(ctx context.Context, ch chan<- AddrUpdate) error {
	return errors.ErrUnsupported
}
