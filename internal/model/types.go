package model

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Asset represents a single free listing card from Fab.
type Asset struct {
	Name  *string `json:"name,omitempty"`
	Link  string  `json:"link"`
	Image *string `json:"image,omitempty"`
}

// DeadlineInfo mirrors the parsed deadline metadata from the scraper.
type DeadlineInfo struct {
	Day       int    `json:"day"`
	Month     int    `json:"month"`
	Year      int    `json:"year"`
	Hour      int    `json:"hour"`
	Minute    int    `json:"minute"`
	GMTOffset string `json:"gmt_offset"`
}

// StoredDeadline preserves the Python payload shape: string, object, or null.
type StoredDeadline struct {
	Structured *DeadlineInfo
	Raw        *string
}

// IsEmpty reports whether the deadline contains no usable data.
func (d StoredDeadline) IsEmpty() bool {
	return d.Structured == nil && d.Raw == nil
}

// MarshalJSON preserves the Python payload shape: string, object, or null.
func (d StoredDeadline) MarshalJSON() ([]byte, error) {
	switch {
	case d.Structured != nil:
		return json.Marshal(d.Structured)
	case d.Raw != nil:
		return json.Marshal(*d.Raw)
	default:
		return []byte("null"), nil
	}
}

// UnmarshalJSON accepts string, object, or null payloads.
func (d *StoredDeadline) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*d = StoredDeadline{}
		return nil
	}

	if trimmed[0] == '"' {
		var raw string
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			return err
		}
		if strings.TrimSpace(raw) == "" {
			*d = StoredDeadline{}
			return nil
		}
		d.Raw = &raw
		d.Structured = nil
		return nil
	}

	var info DeadlineInfo
	if err := json.Unmarshal(trimmed, &info); err != nil {
		return err
	}
	d.Structured = &info
	d.Raw = nil
	return nil
}

// ChannelSubscription is the legacy per-channel subscription record.
type ChannelSubscription struct {
	ID          int64  `json:"id"`
	ShownAssets bool   `json:"shown_assets"`
	Locale      string `json:"locale"`
}

// GuildConfig is the active per-guild notification configuration.
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

// UserProfile stores per-user DM notification preferences.
type UserProfile struct {
	ID          int64  `json:"id"`
	ShownAssets bool   `json:"shown_assets"`
	Locale      string `json:"locale"`
	Subscribed  bool   `json:"subscribed"`
}

// Snapshot matches the persisted runtime state in the Python implementation.
type Snapshot struct {
	GuildConfigs []GuildConfig
	UserProfiles []UserProfile
	Assets       []Asset
	Deadline     StoredDeadline
}

// LegacySnapshot matches the one-time JSON migration payload.
type LegacySnapshot struct {
	Channels     []ChannelSubscription
	UserProfiles []UserProfile
	Assets       []Asset
	Deadline     StoredDeadline
}

