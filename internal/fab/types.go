package fab

import "context"

// Asset matches the shape consumed by the bot runtime.
type Asset struct {
	Name  string
	Link  string
	Image string
}

// DeadlineInfo mirrors the Python TypedDict payload.
type DeadlineInfo struct {
	Day       int
	Month     int
	Year      int
	Hour      int
	Minute    int
	GMTOffset string
}

// Result contains the parsed homepage state.
//
// Found is true when the Limited-Time Free section was located, even if it was
// empty. When Found is false, the caller should retry or treat it as a scrape
// failure.
type Result struct {
	Assets   []Asset
	Deadline *DeadlineInfo
	Found    bool
}

// Getter provides the asset-fetch contract consumed by the runtime.
type Getter interface {
	GetFreeAssets(ctx context.Context) ([]Asset, *DeadlineInfo, error)
}
