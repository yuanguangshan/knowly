package cluster

import "time"

// Config describes clustering behavior.
type Config struct {
	Enabled    bool `json:"enabled"`
	IntervalH  int  `json:"interval_hours"`  // 0 = one-shot at startup
	MinScore   int  `json:"min_score"`       // minimum AI score to include (0–10), 0 = no filter
	MaxEntries int  `json:"max_entries"`     // max entries per run, 0 = default 500
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Enabled:    false,
		IntervalH:  24,
		MinScore:   0,
		MaxEntries: 500,
	}
}

// Cluster is a group of related entries.
type Cluster struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	EntryIDs    []string `json:"entry_ids"`
	CommonTags  []string `json:"common_tags"`
	EntryCount  int      `json:"entry_count"`
}

// ClusterResult is the full output of a single clustering run.
type ClusterResult struct {
	Clusters       []Cluster `json:"clusters"`
	UnclusteredIDs []string  `json:"unclustered_ids"`
	GeneratedAt    time.Time `json:"generated_at"`
	PeriodStart    time.Time `json:"period_start"`
	PeriodEnd      time.Time `json:"period_end"`
	TotalEntries   int       `json:"total_entries"`
	ClusteredCount int       `json:"clustered_count"`
}

// clusterEntry is the per-entry payload sent to the AI for clustering.
type clusterEntry struct {
	ID      string   `json:"id"`
	Tags    []string `json:"tags"`
	Summary string   `json:"summary"`
	Preview string   `json:"preview"`
}
