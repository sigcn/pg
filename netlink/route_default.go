//go:build !linux && !windows && !darwin

package netlink

import (
	"context"
	"errors"
)

func RouteSubscribe(ctx context.Context, ch chan<- RouteUpdate) error {
	return errors.ErrUnsupported
}
