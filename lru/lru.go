package lru

import (
	"container/list"
)

type Cache[K comparable, V any] struct {
	capacity int
	cache    map[K]*list.Element
	list     *list.List
}

type entry[K comparable, V any] struct {
	key   K
	value V
}

func New[K comparable, V any](capacity int) *Cache[K, V] {
	if capacity <= 0 {
		panic("invalid capacity")
	}
	return &Cache[K, V]{
		capacity: capacity,
		cache:    make(map[K]*list.Element),
		list:     list.New(),
	}
}

func (c *Cache[K, V]) Get(key K) (value V, ok bool) {
	if elem, ok := c.cache[key]; ok {
		c.list.MoveToFront(elem)
		return elem.Value.(*entry[K, V]).value, true
	}
	return
}

func (c *Cache[K, V]) Find(filter func(K, V) bool) (key K, value V, ok bool) {
	for k, v := range c.cache {
		if filter(k, v.Value.(*entry[K, V]).value) {
			c.list.MoveToFront(v)
			return k, v.Value.(*entry[K, V]).value, true
		}
	}
	return
}

func (c *Cache[K, V]) Put(key K, value V) {
	if elem, ok := c.cache[key]; ok {
		c.list.MoveToFront(elem)
		elem.Value.(*entry[K, V]).value = value
		return
	}

	if c.list.Len() == c.capacity {
		oldest := c.list.Back()
		if oldest != nil {
			c.list.Remove(oldest)
			delete(c.cache, oldest.Value.(*entry[K, V]).key)
		}
	}

	elem := c.list.PushFront(&entry[K, V]{key: key, value: value})
	c.cache[key] = elem
}
