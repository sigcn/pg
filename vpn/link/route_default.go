//go:build !linux

package link

import (
	"context"
	"errors"
)

func RouteSubscribe(ctx context.Context, ch chan<- RouteUpdate) error {
	return errors.ErrUnsupported
}
