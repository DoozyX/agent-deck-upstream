package costs

import (
	"context"
	"log/slog"
	"os"
	"time"
)

type Fetcher struct {
	CachePath string
	Pricer    *Pricer
	Logger    *slog.Logger
}

func (f *Fetcher) CacheAge() time.Duration {
	info, err := os.Stat(f.CachePath)
	if err != nil {
		return -1
	}
	return time.Since(info.ModTime())
}

// FetchAndCache refreshes bundled, verified pricing metadata. It does not make
// a provider request and records that provenance in the cache.
func (f *Fetcher) FetchAndCache() error {
	if f.Pricer != nil {
		return f.Pricer.saveBundledCache()
	}
	return nil
}

// StartDaily runs the fetch loop. Blocks until context is cancelled.
func (f *Fetcher) StartDaily(ctx context.Context) {
	if f.CacheAge() > 24*time.Hour || f.CacheAge() < 0 {
		if err := f.FetchAndCache(); err != nil && f.Logger != nil {
			f.Logger.Warn("pricing_fetch_failed", slog.String("error", err.Error()))
		}
	}

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := f.FetchAndCache(); err != nil && f.Logger != nil {
				f.Logger.Warn("pricing_fetch_failed", slog.String("error", err.Error()))
			}
		}
	}
}
