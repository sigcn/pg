package cache

import (
	"cmp"
	"sync/atomic"
	"time"
)

type CacheValue[T any] struct {
	updateTime atomic.Pointer[time.Time]
	value      atomic.Value
}

func (v *CacheValue[T]) LoadTTL(ttl time.Duration, newValue func() T) T {
	if time.Since(*cmp.Or(v.updateTime.Load(), &time.Time{})) > cmp.Or(ttl, time.Second) {
		v.value.Store(newValue())
		now := time.Now()
		v.updateTime.Store(&now)
	}
	return v.value.Load().(T)
}

func (v *CacheValue[T]) Load(newValue func() T) T {
	return v.LoadTTL(0, newValue)
}
