package azureauth

import (
	"context"
	"sync"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
)

// memoryCache is a per-operation MSAL cache accessor, never a disk cache. Each
// copy crosses its boundary by value so callers cannot mutate active sessions.
type memoryCache struct {
	mu   sync.Mutex
	data []byte
}

func newMemoryCache(data []byte) *memoryCache {
	return &memoryCache{data: append([]byte(nil), data...)}
}

func (c *memoryCache) Replace(ctx context.Context, target cache.Unmarshaler, _ cache.ReplaceHints) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data := c.snapshot()
	if len(data) == 0 {
		return nil
	}
	return target.Unmarshal(data)
}

func (c *memoryCache) Export(ctx context.Context, source cache.Marshaler, _ cache.ExportHints) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := source.Marshal()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.data = append([]byte(nil), data...)
	c.mu.Unlock()
	return nil
}

func (c *memoryCache) snapshot() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.data...)
}
