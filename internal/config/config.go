package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go-cli-agent/internal/fileutil"

	"gopkg.in/yaml.v3"
)

const (
	legacyIsolationRootDir              = ".go-cli-agent/_worktrees"
	defaultProviderRequestTimeoutSec    = 300
	defaultProviderStreamIdleTimeoutMS  = 300000
	defaultRetryMaxAttempts             = 5
	defaultRetryBaseDelayMS             = 1000
	defaultProviderAutoResumeMaxAttempt = 2
)

type Config struct {
	SchemaVersion   int                 `yaml:"schema_version"`
	DefaultProvider string              `yaml:"default_provider"`
	Providers       map[string]Provider `yaml:"providers"`
	RoleProviders   RoleProvidersConfig `yaml:"role_providers,omitempty"`
	Session         SessionConfig       `yaml:"session"`
	Skills          SkillsConfig        `yaml:"skills"`
	Runtime         RuntimeConfig       `yaml:"runtime"`
	Output          OutputConfig        `yaml:"output"`
	Hooks           HooksConfig         `yaml:"hooks"`
}

type Provider struct {
	APIProvider         string   `yaml:"api_provider,omitempty"`
	APIKeyEnv           string   `yaml:"api_key_env"`
	BaseURL             string   `yaml:"base_url"`
	Model               string   `yaml:"model"`
	TimeoutSec          int      `yaml:"timeout_sec"`
	RequestTimeoutSec   int      `yaml:"request_timeout_sec,omitempty"`
	StreamIdleTimeoutMS int      `yaml:"stream_idle_timeout_ms,omitempty"`
	Retry               Retry    `yaml:"retry,omitempty"`
	AnthropicVersion    string   `yaml:"anthropic_version,omitempty"`
	WireAPI             string   `yaml:"wire_api,omitempty"`
	Temperature         *float64 `yaml:"temperature,omitempty"`
	TopP                *float64 `yaml:"top_p,omitempty"`
	MaxOutputTokens     int      `yaml:"max_output_tokens,omitempty"`
	ReasoningEffort     string   `yaml:"reasoning_effort,omitempty"`
	ReasoningSummary    string   `yaml:"reasoning_summary,omitempty"`
	TextVerbosity       string   `yaml:"text_verbosity,omitempty"`
	ThinkingBudget      int      `yaml:"thinking_budget,omitempty"`
	IncludeThoughts     *bool    `yaml:"include_thoughts,omitempty"`
	Store               *bool    `yaml:"store,omitempty"`
	SendMetadata        *bool    `yaml:"send_metadata,omitempty"`
	RawSidecar          *bool    `yaml:"raw_sidecar,omitempty"`
}

type RoleProvidersConfig struct {
	Planner   RoleProviderOverride `yaml:"planner,omitempty"`
	Generator RoleProviderOverride `yaml:"generator,omitempty"`
	Evaluator RoleProviderOverride `yaml:"evaluator,omitempty"`
}

type RoleProviderOverride struct {
	Provider    string `yaml:"provider,omitempty"`
	APIProvider string `yaml:"api_provider,omitempty"`
	BaseURL     string `yaml:"base_url,omitempty"`
	Model       string `yaml:"model,omitempty"`
}

func (p Provider) ResolvedAPIKey() string {
	if envValue := strings.TrimSpace(os.Getenv(p.APIKeyEnv)); envValue != "" {
		return envValue
	}
	return ""
}

type Retry struct {
	MaxAttempts    int  `yaml:"max_attempts,omitempty"`
	BaseDelayMS    int  `yaml:"base_delay_ms,omitempty"`
	Retry429       bool `yaml:"retry_429,omitempty"`
	Retry5xx       bool `yaml:"retry_5xx,omitempty"`
	RetryTransport bool `yaml:"retry_transport,omitempty"`
}

type SessionConfig struct {
	Dir     string `yaml:"dir"`
	DirMode string `yaml:"dir_mode"`
}

type SkillsConfig struct {
	Dirs []string `yaml:"dirs"`
}

type RuntimeConfig struct {
	ExecFinishRequired bool                     `yaml:"exec_finish_required"`
	MaxTurnsSoft       int                      `yaml:"max_turns_soft"`
	MaxTurnsHard       int                      `yaml:"max_turns_hard"`
	CommandTimeoutSec  int                      `yaml:"command_timeout_sec"`
	GuardrailsMode     string                   `yaml:"guardrails_mode"`
	ProviderAutoResume ProviderAutoResumeConfig `yaml:"provider_auto_resume"`
	Steer              SteerConfig              `yaml:"steer"`
	MultiAgent         MultiAgentConfig         `yaml:"multi_agent"`
	Isolation          IsolationConfig          `yaml:"isolation"`
	Queue              QueueConfig              `yaml:"queue"`
	Shell              ShellConfig              `yaml:"shell"`
	ExecPolicy         ExecPolicyConfig         `yaml:"exec_policy"`
	ShellEnvAllowlist  []string                 `yaml:"shell_env_allowlist"`
	Compact            CompactConfig            `yaml:"compact"`
	Ephemeral          EphemeralConfig          `yaml:"ephemeral"`
	RalphLoop          RalphLoopConfig          `yaml:"ralph_loop"`
	PreCompletion      PreCompletionConfig      `yaml:"pre_completion"`
}

type SteerConfig struct {
	PollIntervalMS  int    `yaml:"poll_interval_ms"`
	DefaultBehavior string `yaml:"default_behavior"`
}

type MultiAgentConfig struct {
	Enabled  bool `yaml:"enabled"`
	MaxDepth int  `yaml:"max_depth"`
}

type IsolationConfig struct {
	DefaultMode string `yaml:"default_mode"`
	RootDir     string `yaml:"root_dir"`
}

type QueueConfig struct {
	PollIntervalMS int  `yaml:"poll_interval_ms"`
	AutoWorker     bool `yaml:"auto_worker"`
}

type ShellConfig struct {
	Sandbox string `yaml:"sandbox,omitempty"`
}

type ExecPolicyConfig struct {
	Mode string `yaml:"mode,omitempty"`
}

type CompactConfig struct {
	InputCharThreshold    int                              `yaml:"input_char_threshold"`
	KeepRecentToolResults int                              `yaml:"keep_recent_tool_results"`
	HysteresisDeltaChars  int                              `yaml:"hysteresis_delta_chars,omitempty"`
	ContextProfiles       map[string]CompactContextProfile `yaml:"context_profiles,omitempty"`
}

type CompactContextProfile struct {
	InputCharThreshold    int `yaml:"input_char_threshold,omitempty"`
	KeepRecentToolResults int `yaml:"keep_recent_tool_results,omitempty"`
	HysteresisDeltaChars  int `yaml:"hysteresis_delta_chars,omitempty"`
}

type ProviderAutoResumeConfig struct {
	Enabled     bool `yaml:"enabled"`
	MaxAttempts int  `yaml:"max_attempts"`
}

type EphemeralConfig struct {
	Enabled     bool   `yaml:"enabled"`
	ArtifactDir string `yaml:"artifact_dir"`
}

type RalphLoopConfig struct {
	Enabled       bool `yaml:"enabled"`
	MaxIterations int  `yaml:"max_iterations"`
}

type PreCompletionConfig struct {
	Enabled       bool `yaml:"enabled"`
	CheckFeatures bool `yaml:"check_features"`
}

type OutputConfig struct {
	Format        string `yaml:"format"`
	ShowRawEvents bool   `yaml:"show_raw_events"`
}

type HooksConfig struct {
	DefaultTimeoutSec int              `yaml:"default_timeout_sec"`
	SessionStart      []HookDefinition `yaml:"session_start"`
	SessionAwaiting   []HookDefinition `yaml:"session_awaiting_input"`
	SessionPause      []HookDefinition `yaml:"session_pause"`
	SessionComplete   []HookDefinition `yaml:"session_complete"`
	SessionFail       []HookDefinition `yaml:"session_fail"`
	UserMessage       []HookDefinition `yaml:"user_message"`
	AssistantMessage  []HookDefinition `yaml:"assistant_message"`
	ToolBefore        []HookDefinition `yaml:"tool_before"`
	ToolAfter         []HookDefinition `yaml:"tool_after"`
}

type HookDefinition struct {
	Name       string      `yaml:"name"`
	Match      HookMatch   `yaml:"match,omitempty"`
	Command    []string    `yaml:"command,omitempty"`
	TimeoutSec int         `yaml:"timeout_sec,omitempty"`
	FailClosed bool        `yaml:"fail_closed,omitempty"`
	Inject     *HookInject `yaml:"inject,omitempty"`
	Filter     *HookFilter `yaml:"filter,omitempty"`
}

type HookMatch struct {
	Tool   string `yaml:"tool,omitempty"`
	Mode   string `yaml:"mode,omitempty"`
	Status string `yaml:"status,omitempty"`
}

type HookInject struct {
	Field  string `yaml:"field"`
	Prefix string `yaml:"prefix,omitempty"`
	Suffix string `yaml:"suffix,omitempty"`
	Set    string `yaml:"set,omitempty"`
}

type HookFilter struct {
	Field            string `yaml:"field"`
	RejectIfContains string `yaml:"reject_if_contains,omitempty"`
}

func Default() *Config {
	return &Config{
		SchemaVersion:   1,
		DefaultProvider: "openai",
		Providers: map[string]Provider{
			"openai": {
				APIProvider:         "openai-compatible",
				APIKeyEnv:           "OPENAI_API_KEY",
				BaseURL:             "https://api.openai.com/v1",
				Model:               "gpt-5.4",
				TimeoutSec:          defaultProviderRequestTimeoutSec,
				RequestTimeoutSec:   defaultProviderRequestTimeoutSec,
				StreamIdleTimeoutMS: defaultProviderStreamIdleTimeoutMS,
				Retry: Retry{
					MaxAttempts:    defaultRetryMaxAttempts,
					BaseDelayMS:    defaultRetryBaseDelayMS,
					Retry5xx:       true,
					RetryTransport: true,
				},
				WireAPI: "responses",
				Store:   boolPtr(false),
			},
			"anthropic": {
				APIProvider:         "anthropic-compatible",
				APIKeyEnv:           "ANTHROPIC_API_KEY",
				BaseURL:             "https://api.anthropic.com",
				Model:               "claude-sonnet-4-6",
				TimeoutSec:          defaultProviderRequestTimeoutSec,
				RequestTimeoutSec:   defaultProviderRequestTimeoutSec,
				StreamIdleTimeoutMS: defaultProviderStreamIdleTimeoutMS,
				Retry: Retry{
					MaxAttempts:    defaultRetryMaxAttempts,
					BaseDelayMS:    defaultRetryBaseDelayMS,
					Retry5xx:       true,
					RetryTransport: true,
				},
				AnthropicVersion: "2023-06-01",
			},
			"google": {
				APIProvider:         "google",
				APIKeyEnv:           "GEMINI_API_KEY",
				BaseURL:             "https://generativelanguage.googleapis.com",
				Model:               "gemini-2.5-flash",
				TimeoutSec:          defaultProviderRequestTimeoutSec,
				RequestTimeoutSec:   defaultProviderRequestTimeoutSec,
				StreamIdleTimeoutMS: defaultProviderStreamIdleTimeoutMS,
				Retry: Retry{
					MaxAttempts:    defaultRetryMaxAttempts,
					BaseDelayMS:    defaultRetryBaseDelayMS,
					Retry5xx:       true,
					RetryTransport: true,
				},
			},
			"openai-compatible": {
				APIProvider:         "openai-compatible",
				APIKeyEnv:           "OPENAI_API_KEY",
				BaseURL:             "http://localhost:3000/v1",
				Model:               "gpt-5.4",
				TimeoutSec:          defaultProviderRequestTimeoutSec,
				RequestTimeoutSec:   defaultProviderRequestTimeoutSec,
				StreamIdleTimeoutMS: defaultProviderStreamIdleTimeoutMS,
				Retry: Retry{
					MaxAttempts:    defaultRetryMaxAttempts,
					BaseDelayMS:    defaultRetryBaseDelayMS,
					Retry5xx:       true,
					RetryTransport: true,
				},
				WireAPI: "responses",
				Store:   boolPtr(false),
			},
		},
		Session: SessionConfig{
			Dir:     ".go-cli-agent/sessions",
			DirMode: "0700",
		},
		Skills: SkillsConfig{
			Dirs: []string{"./skills"},
		},
		Runtime: RuntimeConfig{
			ExecFinishRequired: true,
			MaxTurnsSoft:       24,
			MaxTurnsHard:       40,
			CommandTimeoutSec:  120,
			GuardrailsMode:     "yolo",
			ProviderAutoResume: ProviderAutoResumeConfig{
				Enabled:     true,
				MaxAttempts: defaultProviderAutoResumeMaxAttempt,
			},
			Steer: SteerConfig{
				PollIntervalMS:  250,
				DefaultBehavior: "queue",
			},
			MultiAgent: MultiAgentConfig{
				Enabled:  true,
				MaxDepth: 4,
			},
			Isolation: IsolationConfig{
				DefaultMode: "off",
				RootDir:     defaultIsolationRootDir(),
			},
			Queue: QueueConfig{
				PollIntervalMS: 1000,
				AutoWorker:     true,
			},
			ExecPolicy: ExecPolicyConfig{
				Mode: "warn",
			},
			ShellEnvAllowlist: []string{"PATH", "HOME", "LANG", "TERM"},
			Compact: CompactConfig{
				InputCharThreshold:    160000,
				KeepRecentToolResults: 3,
				HysteresisDeltaChars:  40000,
			},
			Ephemeral: EphemeralConfig{
				Enabled:     true,
				ArtifactDir: ".artifacts/tool-outputs",
			},
			RalphLoop: RalphLoopConfig{
				Enabled:       false,
				MaxIterations: 5,
			},
			PreCompletion: PreCompletionConfig{
				Enabled:       true,
				CheckFeatures: true,
			},
		},
		Output: OutputConfig{
			Format:        "text",
			ShowRawEvents: false,
		},
		Hooks: HooksConfig{
			DefaultTimeoutSec: 15,
		},
	}
}

func Load(explicitPath, cwd string) (*Config, error) {
	cfg := Default()

	loadOrder := []string{}
	if explicitPath == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			loadOrder = append(loadOrder, filepath.Join(home, ".go-cli-agent", "config.yaml"))
		}
		loadOrder = append(loadOrder, filepath.Join(cwd, ".go-cli-agent", "config.yaml"))
		if envPath := os.Getenv("GO_CLI_AGENT_CONFIG"); envPath != "" {
			loadOrder = append(loadOrder, envPath)
		}
	} else {
		loadOrder = append(loadOrder, explicitPath)
	}

	workspaceConfigPath := filepath.Join(cwd, ".go-cli-agent", "config.yaml")
	for _, path := range loadOrder {
		if path == "" {
			continue
		}
		if explicitPath == "" && sameCleanPath(path, workspaceConfigPath) && !workspaceConfigTrusted(cwd) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
	}

	normalizeConfig(cfg, cwd)
	return cfg, nil
}

func PersistPath(explicitPath, cwd string) string {
	if strings.TrimSpace(explicitPath) != "" {
		return resolveMaybeRelative(cwd, explicitPath)
	}
	if envPath := strings.TrimSpace(os.Getenv("GO_CLI_AGENT_CONFIG")); envPath != "" {
		return resolveMaybeRelative(cwd, envPath)
	}
	return filepath.Join(cwd, ".go-cli-agent", "config.yaml")
}

func normalizeConfig(cfg *Config, cwd string) {
	if cfg.DefaultProvider == "" {
		cfg.DefaultProvider = "openai"
	}
	if cfg.Runtime.CommandTimeoutSec <= 0 {
		cfg.Runtime.CommandTimeoutSec = 120
	}
	cfg.Runtime.GuardrailsMode = normalizeGuardrailsMode(cfg.Runtime.GuardrailsMode)
	normalizeRoleProviderOverride(&cfg.RoleProviders.Planner)
	normalizeRoleProviderOverride(&cfg.RoleProviders.Generator)
	normalizeRoleProviderOverride(&cfg.RoleProviders.Evaluator)
	for name, provider := range cfg.Providers {
		provider.APIProvider = normalizeAPIProvider(provider.APIProvider)
		provider.ReasoningSummary = normalizeReasoningSummary(provider.ReasoningSummary)
		normalizeProviderTimeouts(&provider)
		normalizeProviderRetry(&provider)
		if apiProvider, err := EffectiveAPIProvider(name, provider); err == nil && apiProvider == "openai-compatible" && provider.WireAPI == "" {
			provider.WireAPI = "responses"
		}
		cfg.Providers[name] = provider
	}
	if cfg.Runtime.MaxTurnsSoft <= 0 {
		cfg.Runtime.MaxTurnsSoft = 24
	}
	if cfg.Runtime.MaxTurnsHard == 0 {
		cfg.Runtime.MaxTurnsHard = 40
	} else if cfg.Runtime.MaxTurnsHard < 0 {
		cfg.Runtime.MaxTurnsHard = -1
	}
	if cfg.Runtime.Steer.PollIntervalMS <= 0 {
		cfg.Runtime.Steer.PollIntervalMS = 250
	}
	if cfg.Runtime.MultiAgent.MaxDepth <= 0 {
		cfg.Runtime.MultiAgent.MaxDepth = 4
	}
	if strings.TrimSpace(cfg.Runtime.Isolation.DefaultMode) == "" {
		cfg.Runtime.Isolation.DefaultMode = "off"
	}
	if cfg.Runtime.Queue.PollIntervalMS <= 0 {
		cfg.Runtime.Queue.PollIntervalMS = 1000
	}
	cfg.Runtime.Shell.Sandbox = strings.ToLower(strings.TrimSpace(cfg.Runtime.Shell.Sandbox))
	cfg.Runtime.ExecPolicy.Mode = normalizeExecPolicyMode(cfg.Runtime.ExecPolicy.Mode)
	if cfg.Runtime.Compact.InputCharThreshold <= 0 {
		cfg.Runtime.Compact.InputCharThreshold = 160000
	}
	if cfg.Runtime.Compact.KeepRecentToolResults <= 0 {
		cfg.Runtime.Compact.KeepRecentToolResults = 3
	}
	if cfg.Runtime.Compact.HysteresisDeltaChars <= 0 {
		cfg.Runtime.Compact.HysteresisDeltaChars = 40000
	}
	if cfg.Runtime.ProviderAutoResume.MaxAttempts <= 0 {
		cfg.Runtime.ProviderAutoResume.MaxAttempts = defaultProviderAutoResumeMaxAttempt
	}
	if cfg.Runtime.RalphLoop.MaxIterations <= 0 {
		cfg.Runtime.RalphLoop.MaxIterations = 5
	}
	if len(cfg.Runtime.ShellEnvAllowlist) == 0 {
		cfg.Runtime.ShellEnvAllowlist = []string{"PATH", "HOME", "LANG", "TERM"}
	}
	cfg.Session.Dir = resolveMaybeRelative(cwd, cfg.Session.Dir)
	cfg.Runtime.Isolation.RootDir = normalizeIsolationRootDir(cwd, cfg.Runtime.Isolation.RootDir)
	for i, dir := range cfg.Skills.Dirs {
		cfg.Skills.Dirs[i] = resolveMaybeRelative(cwd, dir)
	}
}

func normalizeRoleProviderOverride(override *RoleProviderOverride) {
	if override == nil {
		return
	}
	override.Provider = strings.TrimSpace(override.Provider)
	override.APIProvider = normalizeAPIProvider(override.APIProvider)
	override.BaseURL = strings.TrimSpace(override.BaseURL)
	override.Model = strings.TrimSpace(override.Model)
}

func (c *Config) RoleProviderOverride(role string) RoleProviderOverride {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "planner":
		return c.RoleProviders.Planner
	case "generator":
		return c.RoleProviders.Generator
	case "evaluator":
		return c.RoleProviders.Evaluator
	default:
		return RoleProviderOverride{}
	}
}

func normalizeGuardrailsMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "standard":
		return "standard"
	case "yolo":
		return "yolo"
	default:
		return "standard"
	}
}

func normalizeExecPolicyMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "warn", "warning", "warning-only":
		return "warn"
	case "deny", "block":
		return "deny"
	case "off", "disabled", "none":
		return "off"
	default:
		return "warn"
	}
}

func resolveMaybeRelative(cwd, value string) string {
	if value == "" {
		return value
	}
	value = expandHomeDir(value)
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(cwd, value)
}

func normalizeIsolationRootDir(cwd, value string) string {
	value = strings.TrimSpace(value)
	if value == "" || filepath.Clean(value) == filepath.Clean(legacyIsolationRootDir) {
		return defaultIsolationRootDir()
	}
	return resolveMaybeRelative(cwd, value)
}

func expandHomeDir(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value[0] != '~' {
		return value
	}
	if value != "~" && !strings.HasPrefix(value, "~/") {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return value
	}
	if value == "~" {
		return home
	}
	return filepath.Join(home, value[2:])
}

func defaultIsolationRootDir() string {
	home, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".go-cli-agent", "_worktrees")
	}
	return filepath.Join(os.TempDir(), "go-cli-agent", "_worktrees")
}

func (c *Config) ProviderConfig(name string) (Provider, error) {
	if name == "" {
		name = c.DefaultProvider
	}
	value, ok := c.Providers[name]
	if !ok {
		return Provider{}, errors.New("unknown provider: " + name)
	}
	return value, nil
}

func (c *Config) APIKey(providerName string) string {
	provider, ok := c.Providers[providerName]
	if !ok {
		return ""
	}
	return provider.ResolvedAPIKey()
}

func EffectiveAPIProvider(name string, provider Provider) (string, error) {
	if normalized := normalizeAPIProvider(provider.APIProvider); normalized != "" {
		return normalized, nil
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "openai", "openai-compatible":
		return "openai-compatible", nil
	case "anthropic":
		return "anthropic-compatible", nil
	case "google":
		return "google", nil
	default:
		return "", fmt.Errorf("custom provider %q requires api_provider", name)
	}
}

func normalizeAPIProvider(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "default":
		return ""
	case "openai-compatible", "responses":
		return "openai-compatible"
	case "anthropic-compatible", "anthropic":
		return "anthropic-compatible"
	case "google", "gemini":
		return "google"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizeReasoningSummary(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "default", "provider_default":
		return ""
	case "auto", "concise", "detailed", "none":
		return strings.ToLower(strings.TrimSpace(value))
	case "off":
		return "none"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func ParseFileMode(value string, fallback fs.FileMode) (fs.FileMode, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback & 0o777, nil
	}
	parsed, err := strconv.ParseUint(value, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid file mode %q: %w", value, err)
	}
	return fs.FileMode(parsed) & 0o777, nil
}

func MarshalYAML(cfg *Config) ([]byte, error) {
	return yaml.Marshal(cfg)
}

func WriteFile(path string, cfg *Config) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("config path is required")
	}
	data, err := MarshalYAML(cfg)
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteFileNoSymlink(path, data, 0o600)
}

func sameCleanPath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func workspaceConfigTrusted(cwd string) bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("GO_CLI_AGENT_TRUST_WORKSPACE_CONFIG")), "1") ||
		strings.EqualFold(strings.TrimSpace(os.Getenv("GO_CLI_AGENT_TRUST_WORKSPACE_CONFIG")), "true") {
		return true
	}
	marker := filepath.Join(cwd, ".go-cli-agent", "trusted")
	info, err := os.Lstat(marker)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

func Clone(cfg *Config) (*Config, error) {
	data, err := MarshalYAML(cfg)
	if err != nil {
		return nil, err
	}
	var out Config
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func boolPtr(value bool) *bool {
	return &value
}

func normalizeProviderRetry(retryProvider *Provider) {
	if retryProvider == nil {
		return
	}
	if retryProvider.Retry == (Retry{}) {
		retryProvider.Retry = Retry{
			MaxAttempts:    defaultRetryMaxAttempts,
			BaseDelayMS:    defaultRetryBaseDelayMS,
			Retry5xx:       true,
			RetryTransport: true,
		}
		return
	}
	if retryProvider.Retry.MaxAttempts <= 0 {
		retryProvider.Retry.MaxAttempts = defaultRetryMaxAttempts
	}
	if retryProvider.Retry.BaseDelayMS <= 0 {
		retryProvider.Retry.BaseDelayMS = defaultRetryBaseDelayMS
	}
}

func normalizeProviderTimeouts(provider *Provider) {
	if provider == nil {
		return
	}
	switch {
	case provider.RequestTimeoutSec > 0:
		if provider.TimeoutSec <= 0 {
			provider.TimeoutSec = provider.RequestTimeoutSec
		}
	case provider.TimeoutSec > 0:
		provider.RequestTimeoutSec = provider.TimeoutSec
	default:
		provider.TimeoutSec = defaultProviderRequestTimeoutSec
		provider.RequestTimeoutSec = defaultProviderRequestTimeoutSec
	}
	if provider.StreamIdleTimeoutMS <= 0 {
		provider.StreamIdleTimeoutMS = defaultProviderStreamIdleTimeoutMS
	}
}
