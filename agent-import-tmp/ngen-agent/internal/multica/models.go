package multica

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ngen/internal/provider"
	"ngen/internal/task"
)

type ModelInfo struct {
	ID       string             `json:"id"`
	Label    string             `json:"label"`
	Provider string             `json:"provider,omitempty"`
	Default  bool               `json:"default,omitempty"`
	Thinking *ModelThinkingInfo `json:"thinking,omitempty"`
}

type ModelThinkingInfo struct {
	ConfiguredLevel string `json:"configured_level,omitempty"`
	Source          string `json:"source,omitempty"`
	Provider        string `json:"provider,omitempty"`
}

type EffectiveModel struct {
	Route         string
	ProviderMode  string
	ProviderModel string
}

type ConfigResolution struct {
	Workdir           string
	Config            task.Config
	ConfigSource      string
	ConfigFingerprint string
	EffectiveModel    EffectiveModel
}

func ResolveConfig(workdir, configPath, configScope string) (ConfigResolution, error) {
	if strings.TrimSpace(workdir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ConfigResolution{}, err
		}
		workdir = cwd
	}
	absWorkdir, err := filepath.Abs(workdir)
	if err != nil {
		return ConfigResolution{}, err
	}
	configScope = strings.ToLower(strings.TrimSpace(configScope))
	if configScope == "" {
		configScope = "workspace"
	}
	if configScope != "workspace" && configScope != "daemon" {
		return ConfigResolution{}, fmt.Errorf("unsupported config-scope: %s", configScope)
	}

	cfg, source, err := resolveConfigSource(absWorkdir, configPath, configScope)
	if err != nil {
		return ConfigResolution{}, err
	}
	effective := EffectiveModelFromConfig(cfg)
	return ConfigResolution{
		Workdir:           absWorkdir,
		Config:            cfg,
		ConfigSource:      source,
		ConfigFingerprint: ConfigFingerprint(cfg, source),
		EffectiveModel:    effective,
	}, nil
}

func resolveConfigSource(workdir, configPath, configScope string) (task.Config, string, error) {
	if strings.TrimSpace(configPath) != "" {
		cfg, err := task.LoadConfigFile(configPath)
		if err != nil {
			return task.Config{}, "", err
		}
		return cfg, "daemon:--config", nil
	}
	if envPath := strings.TrimSpace(os.Getenv("NGEN_CONFIG")); envPath != "" {
		cfg, err := task.LoadConfigFile(envPath)
		if err != nil {
			return task.Config{}, "", err
		}
		return cfg, "daemon:NGEN_CONFIG", nil
	}
	if configScope != "daemon" {
		cfg, err := task.LoadConfig(workdir)
		if err != nil {
			return task.Config{}, "", err
		}
		if _, err := os.Stat(filepath.Join(workdir, "ngen.json")); err == nil {
			return cfg, "workspace:ngen.json", nil
		}
		return cfg, "default", nil
	}
	cfg := task.DefaultConfig()
	cfg.Permission.DefaultMode = task.PermissionModeYolo
	normalized, err := task.NormalizeConfig(cfg, nil)
	if err != nil {
		return task.Config{}, "", err
	}
	return normalized, "default:daemon", nil
}

func EffectiveModelFromConfig(cfg task.Config) EffectiveModel {
	mode := provider.CanonicalMode(cfg.Provider.Mode)
	model := strings.TrimSpace(cfg.Provider.Model)
	if model == "" {
		model = "default"
	}
	return EffectiveModel{
		Route:         mode + "/" + model,
		ProviderMode:  mode,
		ProviderModel: model,
	}
}

func ConfigFingerprint(cfg task.Config, source string) string {
	data, _ := json.Marshal(struct {
		Source string      `json:"source"`
		Config task.Config `json:"config"`
	}{
		Source: strings.TrimSpace(source),
		Config: cfg,
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func ListModels(resolution ConfigResolution) []ModelInfo {
	effective := resolution.EffectiveModel
	model := ModelInfo{
		ID:       effective.Route,
		Label:    modelLabel(effective),
		Provider: effective.ProviderMode,
		Default:  true,
	}
	if level := strings.TrimSpace(resolution.Config.Provider.ThinkingLevel); level != "" {
		source := "provider_default"
		if strings.HasPrefix(resolution.ConfigSource, "daemon:") || strings.HasPrefix(resolution.ConfigSource, "default:daemon") {
			source = "daemon_config"
		}
		model.Thinking = &ModelThinkingInfo{
			ConfiguredLevel: level,
			Source:          source,
			Provider:        effective.ProviderMode,
		}
	}
	return []ModelInfo{model}
}

func modelLabel(model EffectiveModel) string {
	if model.ProviderModel == "default" {
		return model.ProviderMode + " default"
	}
	return model.ProviderMode + " " + model.ProviderModel
}
