package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"aegis-agent/internal/fileutil"

	"gopkg.in/yaml.v3"
)

const (
	legacyIsolationRootDir                      = ".aegis-agent/_worktrees"
	defaultProviderRequestTimeoutSec            = 300
	defaultProviderStreamIdleTimeoutMS          = 300000
	defaultRetryMaxAttempts                     = 5
	defaultRetryBaseDelayMS                     = 1000
	defaultProviderAutoResumeMaxAttempt         = 2
	DefaultChildBudgetActiveRuntimeCheckpointMS = 1000
	MinChildBudgetActiveRuntimeCheckpointMS     = 100
	MaxChildBudgetActiveRuntimeCheckpointMS     = 60000
	DefaultToolOutputLLMMaxBytes                = 32 * 1024
	MinToolOutputLLMMaxBytes                    = 512
	MaxToolOutputLLMMaxBytes                    = 1024 * 1024
	DefaultToolOutputDisplayMaxBytes            = 128 * 1024
	MinToolOutputDisplayMaxBytes                = 512
	MaxToolOutputDisplayMaxBytes                = 4 * 1024 * 1024
	DefaultToolOutputArtifactFileMaxBytes       = 16 * 1024 * 1024
	MinToolOutputArtifactFileMaxBytes           = 1024
	MaxToolOutputArtifactFileMaxBytes           = 64 * 1024 * 1024
	DefaultToolOutputArtifactSessionMaxBytes    = 128 * 1024 * 1024
	MinToolOutputArtifactSessionMaxBytes        = 1024
	MaxToolOutputArtifactSessionMaxBytes        = 1024 * 1024 * 1024
	DefaultToolOutputArtifactMaxFiles           = 256
	MinToolOutputArtifactMaxFiles               = 1
	MaxToolOutputArtifactMaxFiles               = 4096
	DefaultCompactKeepRecentToolResultBytes     = 64 * 1024
)

type Config struct {
	SchemaVersion   int                 `yaml:"schema_version"`
	DefaultProvider string              `yaml:"default_provider"`
	Providers       map[string]Provider `yaml:"providers"`
	RoleProviders   RoleProvidersConfig `yaml:"role_providers,omitempty"`
	Web             WebConfig           `yaml:"web,omitempty"`
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
	ContextWindowTokens int      `yaml:"context_window_tokens,omitempty"`
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
	PromptCache         *bool    `yaml:"prompt_cache,omitempty"`
	Store               *bool    `yaml:"store,omitempty"`
	SendMetadata        *bool    `yaml:"send_metadata,omitempty"`
	RawSidecar          *bool    `yaml:"raw_sidecar,omitempty"`
}

type RoleProvidersConfig struct {
	Planner   RoleProviderOverride `yaml:"planner,omitempty"`
	Generator RoleProviderOverride `yaml:"generator,omitempty"`
	Evaluator RoleProviderOverride `yaml:"evaluator,omitempty"`
	Explorer  RoleProviderOverride `yaml:"explorer,omitempty"`
}

type RoleProviderOverride struct {
	Provider        string `yaml:"provider,omitempty"`
	APIProvider     string `yaml:"api_provider,omitempty"`
	BaseURL         string `yaml:"base_url,omitempty"`
	Model           string `yaml:"model,omitempty"`
	ReasoningEffort string `yaml:"reasoning_effort,omitempty"`
	MaxOutputTokens int    `yaml:"max_output_tokens,omitempty"`
}

// WebConfig controls the local WebConsole adapter. The runtime and session
// stores do not depend on these operator-surface settings.
type WebConfig struct {
	BasicAuth WebBasicAuthConfig `yaml:"basic_auth,omitempty"`
}

// WebBasicAuthConfig enables HTTP Basic authentication when both fields are
// set. PasswordHash must be a bcrypt hash; the clear-text password never
// belongs in the configuration file.
type WebBasicAuthConfig struct {
	Username     string `yaml:"username,omitempty"`
	PasswordHash string `yaml:"password_hash,omitempty"`
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
	ChildBudget        ChildBudgetConfig        `yaml:"child_budget"`
	Shell              ShellConfig              `yaml:"shell"`
	ExecPolicy         ExecPolicyConfig         `yaml:"exec_policy"`
	ShellEnvAllowlist  []string                 `yaml:"shell_env_allowlist"`
	Compact            CompactConfig            `yaml:"compact"`
	ToolOutput         ToolOutputConfig         `yaml:"tool_output"`
	Ephemeral          EphemeralConfig          `yaml:"ephemeral"`
	Degeneration       DegenerationConfig       `yaml:"degeneration"`
	RalphLoop          RalphLoopConfig          `yaml:"ralph_loop"`
	PreCompletion      PreCompletionConfig      `yaml:"pre_completion"`
}

type SteerConfig struct {
	PollIntervalMS  int    `yaml:"poll_interval_ms"`
	DefaultBehavior string `yaml:"default_behavior"`
}

type MultiAgentConfig struct {
	Enabled           bool `yaml:"enabled"`
	MaxDepth          int  `yaml:"max_depth"`
	MaxActiveChildren int  `yaml:"max_active_children"`
	CancelGraceSec    int  `yaml:"cancel_grace_sec"`
}

type IsolationConfig struct {
	DefaultMode string `yaml:"default_mode"`
	RootDir     string `yaml:"root_dir"`
}

type QueueConfig struct {
	PollIntervalMS           int  `yaml:"poll_interval_ms"`
	AutoWorker               bool `yaml:"auto_worker"`
	ReaperIntervalMS         int  `yaml:"reaper_interval_ms"`
	LeaseStaleAfterSec       int  `yaml:"lease_stale_after_sec"`
	BackgroundWaitTimeoutSec int  `yaml:"background_wait_timeout_sec"`
}

// ChildBudgetConfig optionally bounds child/background sessions so a single
// delegated run cannot loop indefinitely. It does not apply to root master
// sessions. Canonical dimensions default to zero (disabled). MaxWallClockSec
// and MaxTurns are deprecated read-compatibility aliases migrated during
// normalization; new config writes use the explicit accounting names.
type ChildBudgetConfig struct {
	MaxActiveRuntimeSec       int `yaml:"max_active_runtime_sec"`
	MaxElapsedSec             int `yaml:"max_elapsed_sec"`
	MaxTurnsPerAttempt        int `yaml:"max_turns_per_attempt"`
	ActiveRuntimeCheckpointMS int `yaml:"active_runtime_checkpoint_ms"`
	MaxWallClockSec           int `yaml:"max_wall_clock_sec,omitempty"`
	MaxTurns                  int `yaml:"max_turns,omitempty"`
}

type ShellConfig struct {
	Sandbox string `yaml:"sandbox,omitempty"`
}

type ExecPolicyConfig struct {
	Mode string `yaml:"mode,omitempty"`
}

type CompactConfig struct {
	InputCharThreshold        int                              `yaml:"input_char_threshold"`
	KeepRecentToolResults     int                              `yaml:"keep_recent_tool_results"`
	KeepRecentToolResultBytes int                              `yaml:"keep_recent_tool_result_bytes"`
	HysteresisDeltaChars      int                              `yaml:"hysteresis_delta_chars,omitempty"`
	KeepRecentMessages        int                              `yaml:"keep_recent_messages,omitempty"`
	UtilizationFactor         float64                          `yaml:"utilization_factor,omitempty"`
	SemanticSummary           CompactSemanticSummaryConfig     `yaml:"semantic_summary,omitempty"`
	ContextProfiles           map[string]CompactContextProfile `yaml:"context_profiles,omitempty"`
}

type CompactContextProfile struct {
	InputCharThreshold        int `yaml:"input_char_threshold,omitempty"`
	KeepRecentToolResults     int `yaml:"keep_recent_tool_results,omitempty"`
	KeepRecentToolResultBytes int `yaml:"keep_recent_tool_result_bytes,omitempty"`
	HysteresisDeltaChars      int `yaml:"hysteresis_delta_chars,omitempty"`
	KeepRecentMessages        int `yaml:"keep_recent_messages,omitempty"`
}

type CompactSemanticSummaryConfig struct {
	Enabled       bool `yaml:"enabled"`
	MaxInputChars int  `yaml:"max_input_chars,omitempty"`
	TimeoutSec    int  `yaml:"timeout_sec,omitempty"`
}

type ProviderAutoResumeConfig struct {
	Enabled     bool `yaml:"enabled"`
	MaxAttempts int  `yaml:"max_attempts"`
}

type EphemeralConfig struct {
	Enabled     bool   `yaml:"enabled"`
	ArtifactDir string `yaml:"artifact_dir"`
}

type ToolOutputConfig struct {
	LLMOutputMaxBytes       int `yaml:"llm_output_max_bytes"`
	DisplayOutputMaxBytes   int `yaml:"display_output_max_bytes"`
	ArtifactFileMaxBytes    int `yaml:"artifact_file_max_bytes"`
	ArtifactSessionMaxBytes int `yaml:"artifact_session_max_bytes"`
	ArtifactMaxFiles        int `yaml:"artifact_max_files"`
}

type DegenerationConfig struct {
	Enabled          bool `yaml:"enabled"`
	ReminderAfter    int  `yaml:"reminder_after"`
	GiveUpAfter      int  `yaml:"give_up_after"`
	DetectLowQuality bool `yaml:"detect_low_quality"`
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
			Dir:     ".aegis-agent/sessions",
			DirMode: "0700",
		},
		Skills: SkillsConfig{
			Dirs: []string{"./skills"},
		},
		Runtime: RuntimeConfig{
			ExecFinishRequired: true,
			MaxTurnsSoft:       24,
			MaxTurnsHard:       -1,
			CommandTimeoutSec:  300,
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
				Enabled:           true,
				MaxDepth:          1,
				MaxActiveChildren: 4,
				CancelGraceSec:    5,
			},
			Isolation: IsolationConfig{
				DefaultMode: "off",
				RootDir:     defaultIsolationRootDir(),
			},
			Queue: QueueConfig{
				PollIntervalMS:           1000,
				AutoWorker:               true,
				ReaperIntervalMS:         60000,
				LeaseStaleAfterSec:       900,
				BackgroundWaitTimeoutSec: 0,
			},
			ChildBudget: ChildBudgetConfig{
				MaxActiveRuntimeSec:       0,
				MaxElapsedSec:             0,
				MaxTurnsPerAttempt:        0,
				ActiveRuntimeCheckpointMS: DefaultChildBudgetActiveRuntimeCheckpointMS,
			},
			ExecPolicy: ExecPolicyConfig{
				Mode: "warn",
			},
			ShellEnvAllowlist: []string{"PATH", "HOME", "LANG", "TERM"},
			Compact: CompactConfig{
				KeepRecentToolResults:     3,
				KeepRecentToolResultBytes: DefaultCompactKeepRecentToolResultBytes,
				UtilizationFactor:         DefaultCompactUtilizationFactor,
				SemanticSummary: CompactSemanticSummaryConfig{
					Enabled:       true,
					MaxInputChars: DefaultCompactSemanticSummaryMaxInputChars,
					TimeoutSec:    DefaultCompactSemanticSummaryTimeoutSec,
				},
			},
			ToolOutput: ToolOutputConfig{
				LLMOutputMaxBytes:       DefaultToolOutputLLMMaxBytes,
				DisplayOutputMaxBytes:   DefaultToolOutputDisplayMaxBytes,
				ArtifactFileMaxBytes:    DefaultToolOutputArtifactFileMaxBytes,
				ArtifactSessionMaxBytes: DefaultToolOutputArtifactSessionMaxBytes,
				ArtifactMaxFiles:        DefaultToolOutputArtifactMaxFiles,
			},
			Ephemeral: EphemeralConfig{
				Enabled:     true,
				ArtifactDir: ".artifacts/tool-outputs",
			},
			Degeneration: DegenerationConfig{
				Enabled:       true,
				ReminderAfter: 2,
				GiveUpAfter:   4,
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
			DefaultTimeoutSec: 300,
		},
	}
}

func Load(explicitPath, cwd string) (*Config, error) {
	cfg := Default()

	type loadCandidate struct {
		path                   string
		requiresWorkspaceTrust bool
	}

	loadOrder := []loadCandidate{}
	if explicitPath == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			loadOrder = append(loadOrder, loadCandidate{path: filepath.Join(home, ".aegis-agent", "config.yaml")})
		}
		loadOrder = append(loadOrder, loadCandidate{
			path:                   filepath.Join(cwd, ".aegis-agent", "config.yaml"),
			requiresWorkspaceTrust: true,
		})
		if envPath := os.Getenv("AEGIS_AGENT_CONFIG"); envPath != "" {
			loadOrder = append(loadOrder, loadCandidate{path: envPath})
		}
	} else {
		loadOrder = append(loadOrder, loadCandidate{path: explicitPath})
	}

	for _, candidate := range loadOrder {
		path := candidate.path
		if path == "" {
			continue
		}
		if candidate.requiresWorkspaceTrust && !workspaceConfigTrusted(cwd) {
			continue
		}
		data, _, err := fileutil.ReadRegularFileNoSymlink(path)
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
	if envPath := strings.TrimSpace(os.Getenv("AEGIS_AGENT_CONFIG")); envPath != "" {
		return resolveMaybeRelative(cwd, envPath)
	}
	return filepath.Join(cwd, ".aegis-agent", "config.yaml")
}

func WorkspaceConfigTrusted(cwd string) bool {
	return workspaceConfigTrusted(cwd)
}

func normalizeConfig(cfg *Config, cwd string) {
	if cfg.DefaultProvider == "" {
		cfg.DefaultProvider = "openai"
	}
	if cfg.Runtime.CommandTimeoutSec <= 0 {
		cfg.Runtime.CommandTimeoutSec = 300
	}
	cfg.Runtime.GuardrailsMode = normalizeGuardrailsMode(cfg.Runtime.GuardrailsMode)
	normalizeRoleProviderOverride(&cfg.RoleProviders.Planner)
	normalizeRoleProviderOverride(&cfg.RoleProviders.Generator)
	normalizeRoleProviderOverride(&cfg.RoleProviders.Evaluator)
	normalizeRoleProviderOverride(&cfg.RoleProviders.Explorer)
	for name, provider := range cfg.Providers {
		provider.APIProvider = normalizeAPIProvider(provider.APIProvider)
		provider.ReasoningSummary = normalizeReasoningSummary(provider.ReasoningSummary)
		if provider.ContextWindowTokens < 0 {
			provider.ContextWindowTokens = 0
		}
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
	if cfg.Runtime.MaxTurnsHard <= 0 {
		cfg.Runtime.MaxTurnsHard = -1
	}
	if cfg.Runtime.Steer.PollIntervalMS <= 0 {
		cfg.Runtime.Steer.PollIntervalMS = 250
	}
	if cfg.Runtime.MultiAgent.MaxDepth <= 0 {
		cfg.Runtime.MultiAgent.MaxDepth = 1
	}
	if cfg.Runtime.MultiAgent.MaxActiveChildren <= 0 {
		cfg.Runtime.MultiAgent.MaxActiveChildren = 4
	}
	if cfg.Runtime.MultiAgent.CancelGraceSec <= 0 {
		cfg.Runtime.MultiAgent.CancelGraceSec = 5
	}
	if strings.TrimSpace(cfg.Runtime.Isolation.DefaultMode) == "" {
		cfg.Runtime.Isolation.DefaultMode = "off"
	}
	if cfg.Runtime.Queue.PollIntervalMS <= 0 {
		cfg.Runtime.Queue.PollIntervalMS = 1000
	}
	if cfg.Runtime.Queue.ReaperIntervalMS < 0 {
		cfg.Runtime.Queue.ReaperIntervalMS = 0
	}
	if cfg.Runtime.Queue.LeaseStaleAfterSec < 0 {
		cfg.Runtime.Queue.LeaseStaleAfterSec = 0
	}
	if cfg.Runtime.Queue.BackgroundWaitTimeoutSec < 0 {
		cfg.Runtime.Queue.BackgroundWaitTimeoutSec = 0
	}
	normalizeChildBudget(&cfg.Runtime.ChildBudget)
	cfg.Runtime.Shell.Sandbox = strings.ToLower(strings.TrimSpace(cfg.Runtime.Shell.Sandbox))
	cfg.Runtime.ExecPolicy.Mode = normalizeExecPolicyMode(cfg.Runtime.ExecPolicy.Mode)
	if cfg.Runtime.Compact.KeepRecentToolResults <= 0 {
		cfg.Runtime.Compact.KeepRecentToolResults = 3
	}
	if cfg.Runtime.Compact.KeepRecentToolResultBytes <= 0 {
		cfg.Runtime.Compact.KeepRecentToolResultBytes = DefaultCompactKeepRecentToolResultBytes
	}
	if cfg.Runtime.Compact.UtilizationFactor <= 0 || cfg.Runtime.Compact.UtilizationFactor > 1 {
		cfg.Runtime.Compact.UtilizationFactor = DefaultCompactUtilizationFactor
	}
	if cfg.Runtime.Compact.KeepRecentMessages < 0 {
		cfg.Runtime.Compact.KeepRecentMessages = 0
	}
	if cfg.Runtime.Compact.SemanticSummary.MaxInputChars <= 0 {
		cfg.Runtime.Compact.SemanticSummary.MaxInputChars = DefaultCompactSemanticSummaryMaxInputChars
	}
	if cfg.Runtime.Compact.SemanticSummary.TimeoutSec <= 0 {
		cfg.Runtime.Compact.SemanticSummary.TimeoutSec = DefaultCompactSemanticSummaryTimeoutSec
	}
	normalizeToolOutput(&cfg.Runtime.ToolOutput)
	if cfg.Runtime.ProviderAutoResume.MaxAttempts <= 0 {
		cfg.Runtime.ProviderAutoResume.MaxAttempts = defaultProviderAutoResumeMaxAttempt
	}
	if cfg.Runtime.Degeneration.ReminderAfter <= 0 {
		cfg.Runtime.Degeneration.ReminderAfter = 2
	}
	if cfg.Runtime.Degeneration.GiveUpAfter <= cfg.Runtime.Degeneration.ReminderAfter {
		cfg.Runtime.Degeneration.GiveUpAfter = cfg.Runtime.Degeneration.ReminderAfter + 2
	}
	if cfg.Hooks.DefaultTimeoutSec <= 0 {
		cfg.Hooks.DefaultTimeoutSec = 300
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

func normalizeToolOutput(policy *ToolOutputConfig) {
	if policy == nil {
		return
	}
	policy.LLMOutputMaxBytes = normalizeBoundedPositive(policy.LLMOutputMaxBytes, DefaultToolOutputLLMMaxBytes, MinToolOutputLLMMaxBytes, MaxToolOutputLLMMaxBytes)
	policy.DisplayOutputMaxBytes = normalizeBoundedPositive(policy.DisplayOutputMaxBytes, DefaultToolOutputDisplayMaxBytes, MinToolOutputDisplayMaxBytes, MaxToolOutputDisplayMaxBytes)
	policy.ArtifactFileMaxBytes = normalizeBoundedPositive(policy.ArtifactFileMaxBytes, DefaultToolOutputArtifactFileMaxBytes, MinToolOutputArtifactFileMaxBytes, MaxToolOutputArtifactFileMaxBytes)
	policy.ArtifactSessionMaxBytes = normalizeBoundedPositive(policy.ArtifactSessionMaxBytes, DefaultToolOutputArtifactSessionMaxBytes, MinToolOutputArtifactSessionMaxBytes, MaxToolOutputArtifactSessionMaxBytes)
	policy.ArtifactMaxFiles = normalizeBoundedPositive(policy.ArtifactMaxFiles, DefaultToolOutputArtifactMaxFiles, MinToolOutputArtifactMaxFiles, MaxToolOutputArtifactMaxFiles)
}

func normalizeBoundedPositive(value, fallback, minimum, maximum int) int {
	if value <= 0 {
		return fallback
	}
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func normalizeChildBudget(budget *ChildBudgetConfig) {
	if budget == nil {
		return
	}
	if budget.MaxActiveRuntimeSec <= 0 && budget.MaxWallClockSec > 0 {
		budget.MaxActiveRuntimeSec = budget.MaxWallClockSec
	}
	if budget.MaxTurnsPerAttempt <= 0 && budget.MaxTurns > 0 {
		budget.MaxTurnsPerAttempt = budget.MaxTurns
	}
	if budget.MaxActiveRuntimeSec < 0 {
		budget.MaxActiveRuntimeSec = 0
	}
	if budget.MaxElapsedSec < 0 {
		budget.MaxElapsedSec = 0
	}
	if budget.MaxTurnsPerAttempt < 0 {
		budget.MaxTurnsPerAttempt = 0
	}
	if budget.ActiveRuntimeCheckpointMS <= 0 {
		budget.ActiveRuntimeCheckpointMS = DefaultChildBudgetActiveRuntimeCheckpointMS
	}
	if budget.ActiveRuntimeCheckpointMS < MinChildBudgetActiveRuntimeCheckpointMS {
		budget.ActiveRuntimeCheckpointMS = MinChildBudgetActiveRuntimeCheckpointMS
	}
	if budget.ActiveRuntimeCheckpointMS > MaxChildBudgetActiveRuntimeCheckpointMS {
		budget.ActiveRuntimeCheckpointMS = MaxChildBudgetActiveRuntimeCheckpointMS
	}
	if budget.MaxWallClockSec < 0 {
		budget.MaxWallClockSec = 0
	}
	if budget.MaxTurns < 0 {
		budget.MaxTurns = 0
	}
	// Legacy aliases are read-only compatibility inputs. Once normalized, keep a
	// single canonical in-memory policy so subsequent writes cannot emit both
	// names with diverging values.
	budget.MaxWallClockSec = 0
	budget.MaxTurns = 0
}

func normalizeRoleProviderOverride(override *RoleProviderOverride) {
	if override == nil {
		return
	}
	override.Provider = strings.TrimSpace(override.Provider)
	override.APIProvider = normalizeAPIProvider(override.APIProvider)
	override.BaseURL = strings.TrimSpace(override.BaseURL)
	override.Model = strings.TrimSpace(override.Model)
	override.ReasoningEffort = strings.ToLower(strings.TrimSpace(override.ReasoningEffort))
	if override.MaxOutputTokens < 0 {
		override.MaxOutputTokens = 0
	}
}

func (c *Config) RoleProviderOverride(role string) RoleProviderOverride {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "planner":
		return c.RoleProviders.Planner
	case "generator":
		return c.RoleProviders.Generator
	case "evaluator":
		return c.RoleProviders.Evaluator
	case "explorer":
		return c.RoleProviders.Explorer
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

// ResolvePath applies the same home and invocation-directory expansion used
// when loading path values from the configuration file.
func ResolvePath(cwd, value string) string {
	return resolveMaybeRelative(cwd, value)
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
		return filepath.Join(home, ".aegis-agent", "_worktrees")
	}
	return filepath.Join(os.TempDir(), "aegis-agent", "_worktrees")
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

func workspaceConfigTrusted(_ string) bool {
	value := strings.TrimSpace(os.Getenv("AEGIS_AGENT_TRUST_WORKSPACE_CONFIG"))
	return strings.EqualFold(value, "1") || strings.EqualFold(value, "true")
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
