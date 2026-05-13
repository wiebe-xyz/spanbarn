package cache

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type Store interface {
	Get(ctx context.Context, key string) ([]byte, bool)
	GetStale(ctx context.Context, key string) ([]byte, bool)
	Set(ctx context.Context, key string, data []byte, ttl time.Duration)
	Close() error
}

type Cache struct {
	store Store
	ttl   time.Duration
}

func New(store Store, ttl time.Duration) *Cache {
	return &Cache{store: store, ttl: ttl}
}

func (c *Cache) Close() error {
	return c.store.Close()
}

func Get[T any](c *Cache, ctx context.Context, key string) (T, bool) {
	var zero T
	if c == nil {
		return zero, false
	}
	data, ok := c.store.Get(ctx, key)
	if !ok {
		return zero, false
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return zero, false
	}
	return result, true
}

// GetStale returns cached data even if expired, plus a boolean indicating
// whether the data is still fresh (true) or stale (false).
// Returns (zero, false, false) on cache miss.
func GetStale[T any](c *Cache, ctx context.Context, key string) (value T, found bool, fresh bool) {
	var zero T
	if c == nil {
		return zero, false, false
	}
	data, ok := c.store.Get(ctx, key)
	if ok {
		var result T
		if err := json.Unmarshal(data, &result); err != nil {
			return zero, false, false
		}
		return result, true, true
	}
	data, ok = c.store.GetStale(ctx, key)
	if !ok {
		return zero, false, false
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return zero, false, false
	}
	return result, true, false
}

func Set(c *Cache, ctx context.Context, key string, value any) {
	if c == nil {
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	c.store.Set(ctx, key, data, c.ttl)
}

// swrEnvelope wraps a cached payload with a fresh-until timestamp so that
// callers can implement stale-while-revalidate even against backends (Redis)
// where expired entries are evicted. The store TTL is set to staleTTL, while
// freshness is decided by FreshUntilUnix.
type swrEnvelope struct {
	F int64           `json:"f"`
	P json.RawMessage `json:"p"`
}

// SetSWR stores value with a fresh window of freshTTL and a stale window of staleTTL.
// staleTTL must be >= freshTTL; the entry is evicted after staleTTL.
func SetSWR(c *Cache, ctx context.Context, key string, value any, freshTTL, staleTTL time.Duration) {
	if c == nil {
		return
	}
	if staleTTL < freshTTL {
		staleTTL = freshTTL
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	envelope, err := json.Marshal(swrEnvelope{F: time.Now().Add(freshTTL).Unix(), P: payload})
	if err != nil {
		return
	}
	c.store.Set(ctx, key, envelope, staleTTL)
}

// GetSWR returns the cached value along with whether it is still fresh.
// found=false means cache miss.
func GetSWR[T any](c *Cache, ctx context.Context, key string) (value T, found bool, fresh bool) {
	var zero T
	if c == nil {
		return zero, false, false
	}
	data, ok := c.store.Get(ctx, key)
	if !ok {
		data, ok = c.store.GetStale(ctx, key)
		if !ok {
			return zero, false, false
		}
	}
	var env swrEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return zero, false, false
	}
	var result T
	if err := json.Unmarshal(env.P, &result); err != nil {
		return zero, false, false
	}
	return result, true, time.Now().Unix() < env.F
}

// --- In-memory store ---

type memEntry struct {
	data      []byte
	expiresAt time.Time
}

type MemoryStore struct {
	mu      sync.RWMutex
	entries map[string]memEntry
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{entries: make(map[string]memEntry)}
}

func (m *MemoryStore) Get(_ context.Context, key string) ([]byte, bool) {
	m.mu.RLock()
	entry, ok := m.entries[key]
	m.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.data, true
}

func (m *MemoryStore) GetStale(_ context.Context, key string) ([]byte, bool) {
	m.mu.RLock()
	entry, ok := m.entries[key]
	m.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return entry.data, true
}

func (m *MemoryStore) Set(_ context.Context, key string, data []byte, ttl time.Duration) {
	m.mu.Lock()
	m.entries[key] = memEntry{data: data, expiresAt: time.Now().Add(ttl)}
	m.mu.Unlock()
}

func (m *MemoryStore) Close() error { return nil }

// --- Redis store ---

type RedisStore struct {
	client *redis.Client
}

func NewRedisStore(redisURL string) (*RedisStore, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, err
	}

	return &RedisStore{client: client}, nil
}

func (r *RedisStore) Get(ctx context.Context, key string) ([]byte, bool) {
	data, err := r.client.Get(ctx, key).Bytes()
	if err != nil {
		return nil, false
	}
	return data, true
}

func (r *RedisStore) GetStale(ctx context.Context, key string) ([]byte, bool) {
	return r.Get(ctx, key)
}

func (r *RedisStore) Set(ctx context.Context, key string, data []byte, ttl time.Duration) {
	r.client.Set(ctx, key, data, ttl)
}

func (r *RedisStore) Close() error {
	return r.client.Close()
}
