package config

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/YunusEmreAlps/geko/internal/access"
	"github.com/YunusEmreAlps/geko/internal/notify"
	"github.com/YunusEmreAlps/geko/internal/storage"

	"go.yaml.in/yaml/v3"
)

const DefaultListenAddr = "127.0.0.1:4318"
const defaultConfigPath = "geko.yaml"
const DefaultRetentionDays = 7
const DefaultMaxDBMiB = 256
const DefaultRedaction = "best_effort"

// ServerRedaction is the default redaction once an access token is configured,
// since other people can then read the data.
const ServerRedaction = "strict"
const DefaultIngestConcurrency = 4

type Config struct {
	AccessToken access.Token `yaml:"-"`
	PublicURL   string       `yaml:"public_url"`
	// TraceLinkURL opens a trace in another tool such as Grafana Tempo;
	// {trace_id} is replaced with the trace ID. TraceLinkName labels the link.
	TraceLinkURL  string        `yaml:"trace_link_url"`
	TraceLinkName string        `yaml:"trace_link_name"`
	WebhookURL    string        `yaml:"-"`
	WebhookFormat notify.Format `yaml:"-"`
	MCPTriage     bool          `yaml:"-"`
	// AuditLog is a file that receives one JSON line per change and sign-in,
	// or "-" for standard error.
	AuditLog string `yaml:"-"`
	// TokensFile holds the tokens instead of environment variables and is
	// reloaded when it changes.
	TokensFile    string             `yaml:"-"`
	ListenAddr    string             `yaml:"listen_addr"`
	DashboardAddr string             `yaml:"dashboard_addr"`
	GRPCAddr      string             `yaml:"grpc_addr"`
	DBPath        string             `yaml:"db_path"`
	RetentionDays int                `yaml:"retention_days"`
	MaxDBMiB      int                `yaml:"max_db_mib"`
	IngestSlots   int                `yaml:"ingest_concurrency"`
	OnFull        storage.FullPolicy `yaml:"on_full"`
	Redaction     string             `yaml:"redaction"`
	Source        Source             `yaml:"-"`
}

// Source records where each effective setting came from.
type Source struct {
	DashboardAddr, GRPCAddr, PublicURL                             SettingSource
	FilePath                                                       string
	ListenAddr, DBPath, RetentionDays, MaxDBMiB, OnFull, Redaction SettingSource
	IngestSlots                                                    SettingSource
}

type SettingSource struct {
	Kind     string
	Variable string // Environment variable name, when Kind is env.
}

func Load() (Config, error) {
	configPath := os.Getenv("GEKO_CONFIG")
	optional := configPath == ""
	if optional {
		configPath = defaultConfigPath
	}
	cfg, err := loadFile(configPath, optional)
	if err != nil {
		return Config{}, err
	}
	if path := os.Getenv("GEKO_TOKENS_FILE"); path != "" {
		for _, variable := range []string{"GEKO_ACCESS_TOKEN", "GEKO_INGEST_TOKENS", "GEKO_READ_TOKENS", "GEKO_TRIAGE_TOKENS"} {
			if os.Getenv(variable) != "" {
				return Config{}, fmt.Errorf("set tokens in GEKO_TOKENS_FILE or in %s, not both", variable)
			}
		}
		if cfg.TokensFile, err = filepath.Abs(path); err == nil {
			cfg.AccessToken, err = access.LoadTokenFile(cfg.TokensFile)
		}
	} else {
		cfg.AccessToken, err = access.ParseToken(os.Getenv("GEKO_ACCESS_TOKEN"))
		if err == nil {
			cfg.AccessToken, err = cfg.AccessToken.WithIngestTokens(os.Getenv("GEKO_INGEST_TOKENS"))
		}
		if err == nil {
			cfg.AccessToken, err = cfg.AccessToken.WithReadTokens(os.Getenv("GEKO_READ_TOKENS"))
		}
		if err == nil {
			cfg.AccessToken, err = cfg.AccessToken.WithTriageTokens(os.Getenv("GEKO_TRIAGE_TOKENS"))
		}
	}
	if err != nil {
		return Config{}, err
	}
	if cfg.AccessToken, err = proxyAuth(cfg.AccessToken); err != nil {
		return Config{}, err
	}
	cfg.AuditLog = os.Getenv("GEKO_AUDIT_LOG")
	allowPlaintext := false
	if value := os.Getenv("GEKO_ALLOW_PLAINTEXT"); value != "" {
		if allowPlaintext, err = strconv.ParseBool(value); err != nil {
			return Config{}, errors.New("GEKO_ALLOW_PLAINTEXT must be true or false")
		}
	}
	// Geko serves plain HTTP and gRPC. A non-loopback listener needs a token and an
	// explicit acknowledgment that TLS is terminated in front of it.
	remote := func(key, env string, address netip.Addr) error {
		if address.IsLoopback() {
			return nil
		}
		if !cfg.AccessToken.Enabled() {
			return fmt.Errorf("%s (%s) must use a loopback IP unless GEKO_ACCESS_TOKEN is configured", key, env)
		}
		if !allowPlaintext {
			return fmt.Errorf("%s (%s) is not a loopback IP and Geko does not terminate TLS; "+
				"serve it behind a TLS reverse proxy or on a trusted network, then set GEKO_ALLOW_PLAINTEXT=true", key, env)
		}
		return nil
	}

	addr := cfg.ListenAddr
	if override := envOverride("GEKO_LISTEN_ADDR", &cfg.Source.ListenAddr); override != "" {
		addr = override
	}
	if addr == "" {
		addr = DefaultListenAddr
	}
	endpoint, err := netip.ParseAddrPort(addr)
	if err != nil || endpoint.Addr().Zone() != "" || endpoint.Port() == 0 {
		return Config{}, fmt.Errorf("listen_addr (GEKO_LISTEN_ADDR) %q must be an IP address and a nonzero port (for example, %s)", addr, DefaultListenAddr)
	}
	if err := remote("listen_addr", "GEKO_LISTEN_ADDR", endpoint.Addr()); err != nil {
		return Config{}, err
	}

	path := cfg.DBPath
	if override := envOverride("GEKO_DB_PATH", &cfg.Source.DBPath); override != "" {
		path = override
	} else if path != "" && !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(configPath), path)
	}
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return Config{}, fmt.Errorf("locate user configuration directory (or set db_path or GEKO_DB_PATH): %w", err)
		}
		path = filepath.Join(dir, "geko", "geko.db")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("resolve db_path (GEKO_DB_PATH): %w", err)
	}

	cfg.ListenAddr, cfg.DBPath = endpoint.String(), path
	for _, setting := range []struct {
		key, env string
		value    *string
		source   *SettingSource
	}{
		{"dashboard_addr", "GEKO_DASHBOARD_ADDR", &cfg.DashboardAddr, &cfg.Source.DashboardAddr},
		{"grpc_addr", "GEKO_GRPC_ADDR", &cfg.GRPCAddr, &cfg.Source.GRPCAddr},
	} {
		if value, ok := os.LookupEnv(setting.env); ok {
			*setting.value = value
			*setting.source = SettingSource{Kind: "env", Variable: setting.env}
		}
		if *setting.value == "" {
			continue
		}
		address, err := netip.ParseAddrPort(*setting.value)
		if err != nil || address.Addr().Zone() != "" || address.Port() == 0 {
			return Config{}, fmt.Errorf("%s (%s) must be an IP address and a nonzero port", setting.key, setting.env)
		}
		if err := remote(setting.key, setting.env, address.Addr()); err != nil {
			return Config{}, err
		}
		*setting.value = address.String()
	}
	if cfg.DashboardAddr != "" && cfg.DashboardAddr == cfg.ListenAddr {
		return Config{}, errors.New("dashboard_addr must differ from listen_addr; omit it to share the HTTP listener")
	}
	if cfg.GRPCAddr != "" && (cfg.GRPCAddr == cfg.ListenAddr || cfg.GRPCAddr == cfg.DashboardAddr) {
		return Config{}, errors.New("grpc_addr must differ from the HTTP and dashboard listeners")
	}
	if override := envOverride("GEKO_PUBLIC_URL", &cfg.Source.PublicURL); override != "" {
		cfg.PublicURL = override
	}
	if cfg.PublicURL != "" {
		if cfg.PublicURL, err = publicURL(cfg.PublicURL); err != nil {
			return Config{}, err
		}
	}
	if value := os.Getenv("GEKO_TRACE_LINK_URL"); value != "" {
		cfg.TraceLinkURL = value
	}
	if value := os.Getenv("GEKO_TRACE_LINK_NAME"); value != "" {
		cfg.TraceLinkName = value
	}
	if cfg.TraceLinkURL, cfg.TraceLinkName, err = traceLink(cfg.TraceLinkURL, cfg.TraceLinkName); err != nil {
		return Config{}, err
	}
	if cfg.WebhookURL = os.Getenv("GEKO_WEBHOOK_URL"); cfg.WebhookURL != "" {
		parsed, err := url.Parse(cfg.WebhookURL)
		if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
			return Config{}, errors.New("GEKO_WEBHOOK_URL must be an http or https URL")
		}
		if cfg.WebhookFormat, err = notify.ParseFormat(os.Getenv("GEKO_WEBHOOK_FORMAT"), cfg.WebhookURL); err != nil {
			return Config{}, fmt.Errorf("GEKO_WEBHOOK_FORMAT: %w", err)
		}
	} else if os.Getenv("GEKO_WEBHOOK_FORMAT") != "" {
		return Config{}, errors.New("GEKO_WEBHOOK_FORMAT requires GEKO_WEBHOOK_URL")
	}
	if value := os.Getenv("GEKO_MCP_TRIAGE"); value != "" {
		if cfg.MCPTriage, err = strconv.ParseBool(value); err != nil {
			return Config{}, errors.New("GEKO_MCP_TRIAGE must be true or false")
		}
	}
	cfg.RetentionDays, err = intSetting("retention_days", "GEKO_RETENTION_DAYS", &cfg.Source.RetentionDays, cfg.RetentionDays, 1, 3650)
	if err != nil {
		return Config{}, err
	}
	cfg.MaxDBMiB, err = intSetting("max_db_mib", "GEKO_MAX_DB_MIB", &cfg.Source.MaxDBMiB, cfg.MaxDBMiB, 8, 65536)
	if err != nil {
		return Config{}, err
	}
	cfg.IngestSlots, err = intSetting("ingest_concurrency", "GEKO_INGEST_CONCURRENCY", &cfg.Source.IngestSlots, cfg.IngestSlots, 1, 64)
	if err != nil {
		return Config{}, err
	}
	if override := envOverride("GEKO_ON_FULL", &cfg.Source.OnFull); override != "" {
		cfg.OnFull = storage.FullPolicy(override)
	}
	if !cfg.OnFull.Evicts() && cfg.OnFull != storage.Reject {
		return Config{}, fmt.Errorf("on_full (GEKO_ON_FULL) must be %q, %q or %q", storage.EvictOldest, storage.EvictFair, storage.Reject)
	}
	if override := envOverride("GEKO_REDACTION", &cfg.Source.Redaction); override != "" {
		cfg.Redaction = override
	} else if cfg.Source.Redaction.Kind == "default" && cfg.AccessToken.Enabled() {
		cfg.Redaction, cfg.Source.Redaction = ServerRedaction, SettingSource{Kind: "default with an access token"}
	}
	if cfg.Redaction != DefaultRedaction && cfg.Redaction != "strict" {
		return Config{}, errors.New("redaction (GEKO_REDACTION) must be best_effort or strict")
	}
	return cfg, nil
}

// proxyAuth trusts users named by an authenticating proxy when
// GEKO_PROXY_USER_HEADER is set.
func proxyAuth(token access.Token) (access.Token, error) {
	header := os.Getenv("GEKO_PROXY_USER_HEADER")
	if header == "" {
		for _, variable := range []string{"GEKO_PROXY_TRUSTED_IPS", "GEKO_PROXY_ROLES", "GEKO_PROXY_DEFAULT_ROLE"} {
			if os.Getenv(variable) != "" {
				return access.Token{}, fmt.Errorf("%s requires GEKO_PROXY_USER_HEADER", variable)
			}
		}
		return token, nil
	}
	trusted, err := access.ParsePrefixes(os.Getenv("GEKO_PROXY_TRUSTED_IPS"))
	if err != nil {
		return access.Token{}, fmt.Errorf("GEKO_PROXY_TRUSTED_IPS: %w", err)
	}
	roles, err := access.ParseProxyRoles(os.Getenv("GEKO_PROXY_ROLES"))
	if err != nil {
		return access.Token{}, err
	}
	defaultRole := access.RoleViewer
	switch value := os.Getenv("GEKO_PROXY_DEFAULT_ROLE"); value {
	case "":
	case "none":
		defaultRole = 0
	default:
		if defaultRole, err = access.ParseRole(value); err != nil {
			return access.Token{}, fmt.Errorf("GEKO_PROXY_DEFAULT_ROLE: %w, or none", err)
		}
	}
	return token.WithProxy(access.ProxyAuth{Header: header, Trusted: trusted, Roles: roles, Default: defaultRole})
}

// publicURL validates the external base URL, such as https://geko.example.com.
func publicURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", errors.New("public_url (GEKO_PUBLIC_URL) must be an http or https URL without credentials, query or fragment")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

// traceLink validates a trace link template and defaults its name to Tempo.
func traceLink(template, name string) (string, string, error) {
	template, name = strings.TrimSpace(template), strings.TrimSpace(name)
	if template == "" {
		if name != "" {
			return "", "", errors.New("trace_link_name (GEKO_TRACE_LINK_NAME) requires trace_link_url")
		}
		return "", "", nil
	}
	sample := strings.NewReplacer("{trace_id}", "0", "%7Btrace_id%7D", "0", "%7btrace_id%7d", "0").Replace(template)
	parsed, err := url.Parse(sample)
	if sample == template || err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return "", "", errors.New("trace_link_url (GEKO_TRACE_LINK_URL) must be an http or https URL containing {trace_id}")
	}
	if name == "" {
		name = "Tempo"
	}
	if len(name) > 40 {
		return "", "", errors.New("trace_link_name (GEKO_TRACE_LINK_NAME) is limited to 40 bytes")
	}
	return template, name, nil
}

func envOverride(name string, source *SettingSource) string {
	value := os.Getenv(name)
	if value != "" {
		*source = SettingSource{Kind: "env", Variable: name}
	}
	return value
}

func intSetting(key, env string, source *SettingSource, value, minValue, maxValue int) (int, error) {
	var err error
	if override := envOverride(env, source); override != "" {
		value, err = strconv.Atoi(override)
	}
	if err != nil || value < minValue || value > maxValue {
		return 0, fmt.Errorf("%s (%s) must be an integer from %d to %d", key, env, minValue, maxValue)
	}
	return value, nil
}

func loadFile(path string, optional bool) (Config, error) {
	defaultSource := SettingSource{Kind: "default"}
	cfg := Config{
		RetentionDays: DefaultRetentionDays, MaxDBMiB: DefaultMaxDBMiB, IngestSlots: DefaultIngestConcurrency,
		OnFull: storage.EvictOldest, Redaction: DefaultRedaction,
		Source: Source{
			IngestSlots:   defaultSource,
			DashboardAddr: defaultSource, GRPCAddr: defaultSource, PublicURL: defaultSource,
			ListenAddr: defaultSource, DBPath: defaultSource, RetentionDays: defaultSource,
			MaxDBMiB: defaultSource, OnFull: defaultSource, Redaction: defaultSource,
		},
	}
	file, err := os.Open(path)
	if optional && errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return cfg, fmt.Errorf("resolve config path %q: %w", path, err)
	}
	cfg.Source.FilePath = absolutePath

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var values struct {
		DashboardAddr *string             `yaml:"dashboard_addr"`
		GRPCAddr      *string             `yaml:"grpc_addr"`
		PublicURL     *string             `yaml:"public_url"`
		ListenAddr    *string             `yaml:"listen_addr"`
		DBPath        *string             `yaml:"db_path"`
		RetentionDays *int                `yaml:"retention_days"`
		MaxDBMiB      *int                `yaml:"max_db_mib"`
		IngestSlots   *int                `yaml:"ingest_concurrency"`
		OnFull        *storage.FullPolicy `yaml:"on_full"`
		Redaction     *string             `yaml:"redaction"`
		TraceLinkURL  *string             `yaml:"trace_link_url"`
		TraceLinkName *string             `yaml:"trace_link_name"`
	}
	err = decoder.Decode(&values)
	if errors.Is(err, io.EOF) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	var extra yaml.Node
	err = decoder.Decode(&extra)
	if err == nil {
		return Config{}, fmt.Errorf("config %q must contain a single YAML document", path)
	}
	if !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	// Empty listener and path values retain their historical default behavior.
	if values.ListenAddr != nil && *values.ListenAddr != "" {
		yamlSetting(values.ListenAddr, &cfg.ListenAddr, &cfg.Source.ListenAddr)
	}
	if values.DBPath != nil && *values.DBPath != "" {
		yamlSetting(values.DBPath, &cfg.DBPath, &cfg.Source.DBPath)
	}
	yamlSetting(values.RetentionDays, &cfg.RetentionDays, &cfg.Source.RetentionDays)
	yamlSetting(values.DashboardAddr, &cfg.DashboardAddr, &cfg.Source.DashboardAddr)
	yamlSetting(values.GRPCAddr, &cfg.GRPCAddr, &cfg.Source.GRPCAddr)
	yamlSetting(values.PublicURL, &cfg.PublicURL, &cfg.Source.PublicURL)
	yamlSetting(values.MaxDBMiB, &cfg.MaxDBMiB, &cfg.Source.MaxDBMiB)
	yamlSetting(values.IngestSlots, &cfg.IngestSlots, &cfg.Source.IngestSlots)
	yamlSetting(values.OnFull, &cfg.OnFull, &cfg.Source.OnFull)
	yamlSetting(values.Redaction, &cfg.Redaction, &cfg.Source.Redaction)
	if values.TraceLinkURL != nil {
		cfg.TraceLinkURL = *values.TraceLinkURL
	}
	if values.TraceLinkName != nil {
		cfg.TraceLinkName = *values.TraceLinkName
	}
	return cfg, nil
}

func yamlSetting[T any](value *T, target *T, source *SettingSource) {
	if value != nil {
		*target = *value
		*source = SettingSource{Kind: "yaml"}
	}
}
