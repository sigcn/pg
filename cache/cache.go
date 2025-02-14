package cache

import (
	"cmp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sigcn/pg/cache/lru"
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

var (
	defaultCache      *lru.Cache[string, *CacheValue[any]]
	defaultCacheMutex sync.RWMutex
)

func init() {
	defaultCache = lru.New[string, *CacheValue[any]](1024)
}

// LoadTTL load value by key from default cache pool
// if value's ttl is expired, will exec {newValue} create a new value
func LoadTTL[T any](key string, ttl time.Duration, newValue func(key string) T) T {
	defaultCacheMutex.RLock()
	val, ok := defaultCache.Get(key)
	defaultCacheMutex.RUnlock()
	if !ok {
		defaultCacheMutex.Lock()
		val, ok = defaultCache.Get(key)
		if !ok {
			val = &CacheValue[any]{}
			defaultCache.Put(key, val)
		}
		defaultCacheMutex.Unlock()
	}
	ret := val.LoadTTL(ttl, func() any {
		return newValue(key)
	})
	return ret.(T)
}

// Load load value by key from default cache pool
// uses a default ttl value
func Load[T any](key string, newValue func(key string) T) T {
	return LoadTTL(key, 0, newValue)
}
