package tools

import "testing"

func TestExecPolicyDetectsSudo(t *testing.T) {
	violations := DetectExecPolicyViolations("sudo systemctl restart ssh")
	if !hasExecPolicyCategory(violations, "privilege_escalation") {
		t.Fatalf("expected privilege escalation violation, got %#v", violations)
	}
}

func TestExecPolicyDetectsRmRfRoot(t *testing.T) {
	violations := DetectExecPolicyViolations("rm -rf /")
	if !hasExecPolicyCategory(violations, "destructive") {
		t.Fatalf("expected destructive violation, got %#v", violations)
	}
}

func TestExecPolicyDetectsAbsoluteCommandPaths(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		category string
	}{
		{name: "sudo", command: "/usr/bin/sudo systemctl restart ssh", category: "privilege_escalation"},
		{name: "rm", command: "/bin/rm -rf /", category: "destructive"},
		{name: "curl", command: "/usr/bin/curl https://example.com", category: "network_egress"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			violations := DetectExecPolicyViolations(tt.command)
			if !hasExecPolicyCategory(violations, tt.category) {
				t.Fatalf("expected %s violation for %q, got %#v", tt.category, tt.command, violations)
			}
		})
	}
}

func TestExecPolicyDetectsWrappedPolicyCommands(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		category string
	}{
		{name: "env sudo", command: "env sudo systemctl restart ssh", category: "privilege_escalation"},
		{name: "assignment curl", command: "FOO=bar curl https://example.com", category: "network_egress"},
		{name: "env rm", command: "env rm -rf /", category: "destructive"},
		{name: "command cp", command: "command cp token.txt .env.local", category: "secret_path_write"},
		{name: "command path cp", command: "command -p /bin/cp token.txt .env.local", category: "secret_path_write"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			violations := DetectExecPolicyViolations(tt.command)
			if !hasExecPolicyCategory(violations, tt.category) {
				t.Fatalf("expected %s violation for %q, got %#v", tt.category, tt.command, violations)
			}
		})
	}
}

func TestExecPolicyDetectsSecretPathWrite(t *testing.T) {
	for _, command := range []string{
		"echo token > .env",
		"echo token > .env.local",
		"echo token > .env/token",
		"printf key > id_ecdsa",
		"printf key > deploy.pem",
		"printf key > service_private_key.json",
		"printf token > credentials.json",
		"printf token > service-account_credentials.json",
		"printf token > .azure/accessTokens.json",
		"printf token > .oci/config",
		"printf token > .config/gcloud/configurations/config_default",
		"printf token > .npmrc",
		"printf token > .yarnrc.yml",
		"printf token > .pnpmrc",
		"printf token > .m2/settings.xml",
		"printf token > .m2/settings-security.xml",
		"printf token > .gradle/gradle.properties",
		"printf token > .nuget/NuGet.Config",
		"printf token > .config/pip/pip.conf",
	} {
		t.Run(command, func(t *testing.T) {
			violations := DetectExecPolicyViolations(command)
			if !hasExecPolicyCategory(violations, "secret_path_write") {
				t.Fatalf("expected secret path write violation for %q, got %#v", command, violations)
			}
		})
	}
}

func TestExecPolicyDetectsSecretPathWriteCommands(t *testing.T) {
	for _, command := range []string{
		"cp token.txt .env.local",
		"mv token.txt .ssh/id_rsa",
		"touch .aws/credentials",
		"mkdir -p .kube",
		"install -m 600 token.txt .config/gcloud/application_default_credentials.json",
		"cp --target-directory=.ssh token.txt",
		"install -d .config/gcloud",
		"env cp token.txt .env.local",
		"env FOO=bar /bin/cp token.txt .env.local",
		"touch .m2/settings.xml",
		"cp token.txt .pnpmrc",
		"mv token.txt .nuget/NuGet.Config",
		"install -m 600 token.txt .gradle/gradle.properties",
	} {
		t.Run(command, func(t *testing.T) {
			violations := DetectExecPolicyViolations(command)
			if !hasExecPolicyCategory(violations, "secret_path_write") {
				t.Fatalf("expected secret path write violation for %q, got %#v", command, violations)
			}
		})
	}
}

func TestExecPolicyAllowsEnvTemplateWriteCommands(t *testing.T) {
	for _, command := range []string{
		"cp token.txt .env.example",
		"mv token.txt .env.sample",
		"touch .env.template",
	} {
		t.Run(command, func(t *testing.T) {
			violations := DetectExecPolicyViolations(command)
			if hasExecPolicyCategory(violations, "secret_path_write") {
				t.Fatalf("expected env template write command to be allowed for %q, got %#v", command, violations)
			}
		})
	}
}

func TestExecPolicyDetectsSecretPathWriteFromLaterTeeTargets(t *testing.T) {
	for _, command := range []string{
		"printf token | tee reports/out.txt .env.local",
		"printf token | tee -a reports/out.txt .env/token",
		"printf token | /usr/bin/tee reports/out.txt configs/.env.production/token",
		"printf token | tee reports/out.txt .m2/settings.xml",
		"printf token | tee -a reports/out.txt .config/pip/pip.conf",
	} {
		t.Run(command, func(t *testing.T) {
			violations := DetectExecPolicyViolations(command)
			if !hasExecPolicyCategory(violations, "secret_path_write") {
				t.Fatalf("expected secret path write violation for %q, got %#v", command, violations)
			}
		})
	}
}

func TestExecPolicyAllowsEnvTemplateWrites(t *testing.T) {
	for _, command := range []string{
		"echo token > .env.example",
		"echo token > .env.sample",
		"echo token > .env.template",
	} {
		t.Run(command, func(t *testing.T) {
			violations := DetectExecPolicyViolations(command)
			if hasExecPolicyCategory(violations, "secret_path_write") {
				t.Fatalf("expected env template write to be allowed for %q, got %#v", command, violations)
			}
		})
	}
}

func TestExecPolicyAllowsEnvTemplateLaterTeeTargets(t *testing.T) {
	for _, command := range []string{
		"printf token | tee reports/out.txt .env.example",
		"printf token | tee reports/out.txt .env.sample",
		"printf token | tee reports/out.txt .env.template",
	} {
		t.Run(command, func(t *testing.T) {
			violations := DetectExecPolicyViolations(command)
			if hasExecPolicyCategory(violations, "secret_path_write") {
				t.Fatalf("expected env template write to be allowed for %q, got %#v", command, violations)
			}
		})
	}
}

func hasExecPolicyCategory(violations []ExecPolicyViolation, category string) bool {
	for _, violation := range violations {
		if violation.Category == category {
			return true
		}
	}
	return false
}
