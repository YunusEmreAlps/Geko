package config

import "github.com/YunusEmreAlps/geko/internal/budget"

// Pointer fields retain effective YAML origins, including merges and aliases,
// without tying source information to live-editor eligibility. The normal
// KnownFields decoder continues to validate the entire nested configuration.
type budgetFileConfig struct {
	Enabled  bool `yaml:"enabled"`
	Defaults struct {
		Spans *int64 `yaml:"spans_per_hour"`
		Bytes *int64 `yaml:"bytes_per_hour"`
	} `yaml:"default"`
	Services    *[]budget.ServiceLimit `yaml:"services"`
	Attributes  []string               `yaml:"attributes"`
	Cardinality *int                   `yaml:"cardinality_limit"`
	HistoryDays *int                   `yaml:"history_days"`
}

func (f budgetFileConfig) effective() (budget.Config, BudgetSources) {
	cfg := budget.Config{Enabled: f.Enabled, Attributes: f.Attributes, HistoryDays: 0}
	if f.HistoryDays != nil {
		cfg.HistoryDays = *f.HistoryDays
	}
	sources := defaultBudgetSources()
	y := SettingSource{Kind: "yaml"}
	if f.Defaults.Spans != nil {
		cfg.Defaults.Spans = *f.Defaults.Spans
		sources.DefaultSpans = y
	}
	if f.Defaults.Bytes != nil {
		cfg.Defaults.Bytes = *f.Defaults.Bytes
		sources.DefaultBytes = y
	}
	if f.Services != nil {
		cfg.Services = *f.Services
		sources.Services = y
	}
	if f.Cardinality != nil {
		cfg.CardinalityLimit = *f.Cardinality
		sources.Cardinality = y
	}
	return cfg, sources
}

func defaultBudgetSources() BudgetSources {
	d := SettingSource{Kind: "default"}
	return BudgetSources{d, d, d, d}
}
