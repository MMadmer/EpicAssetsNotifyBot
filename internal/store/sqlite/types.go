package sqlite

// ChannelSubscription mirrors the legacy channel_subscriptions table.
type ChannelSubscription struct {
	ID          int64  `json:"id"`
	ShownAssets bool   `json:"shown_assets"`
	Locale      string `json:"locale"`
}

// GuildConfig mirrors the runtime guild_configs table.
type GuildConfig struct {
	GuildID       int64  `json:"guild_id"`
	ChannelID     *int64 `json:"channel_id,omitempty"`
	ThreadID      *int64 `json:"thread_id,omitempty"`
	ShownAssets   bool   `json:"shown_assets"`
	Locale        string `json:"locale"`
	MentionRoleID *int64 `json:"mention_role_id,omitempty"`
	Enabled       bool   `json:"enabled"`
	IncludeImages bool   `json:"include_images"`
}

// UserProfile mirrors the user_profiles table.
type UserProfile struct {
	ID          int64  `json:"id"`
	ShownAssets bool   `json:"shown_assets"`
	Locale      string `json:"locale"`
	Subscribed  bool   `json:"subscribed"`
}

// Asset mirrors the current_assets table.
type Asset struct {
	Name  *string `json:"name,omitempty"`
	Link  string  `json:"link"`
	Image *string `json:"image,omitempty"`
}

// DeadlineInfo mirrors the structured deadline payload used by the Python bot.
type DeadlineInfo struct {
	Day       int    `json:"day"`
	Month     int    `json:"month"`
	Year      int    `json:"year"`
	Hour      int    `json:"hour"`
	Minute    int    `json:"minute"`
	GMTOffset string `json:"gmt_offset"`
}

// Snapshot is the runtime state persisted by save_snapshot/load_snapshot.
type Snapshot struct {
	GuildConfigs []GuildConfig `json:"guild_configs"`
	UserProfiles []UserProfile  `json:"user_profiles"`
	Assets       []Asset        `json:"assets"`
	Deadline     any            `json:"deadline"`
}

// LegacySnapshot matches the one-time JSON import payload.
type LegacySnapshot struct {
	Channels     []ChannelSubscription `json:"channels"`
	UserProfiles []UserProfile         `json:"user_profiles"`
	Assets       []Asset               `json:"assets"`
	Deadline     any                   `json:"deadline"`
}
