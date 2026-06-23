package filechanges

import "testing"

func TestCollectShellRedirectTargetsIgnoresHereDocBodies(t *testing.T) {
	command := joinLines(
		"{",
		"  cat > reports/glata-staging/evidence/static-file-sample.txt <<'MD'",
		"- `GET /status` -> `{status}`",
		"- template target should not become {r.get(status)} or \\]+ or ]+",
		"MD",
		"  python3 - <<'PY'",
		"pat=r'''(?:(?:https?:)?//[^\\s\"'<>\\\\]+|/[A-Za-z0-9_./?&=%#:@+-]{2,})'''",
		"print(f'`{r.get(\"status\")}` {r.get(\"file\",\"\")} -> {r.get(\"status\")}')",
		"PY",
		"} > reports/glata-staging/evidence/file-list-current.txt 2>&1",
		"printf 'saved file-list-current.txt\\n'",
	)

	targets := CollectShellRedirectTargets(command)
	byPath := map[string]ShellRedirectTarget{}
	for _, target := range targets {
		byPath[target.Path] = target
	}
	for _, want := range []string{
		"reports/glata-staging/evidence/static-file-sample.txt",
		"reports/glata-staging/evidence/file-list-current.txt",
	} {
		if _, ok := byPath[want]; !ok {
			t.Fatalf("expected redirect target %q in %#v", want, targets)
		}
	}
	for _, bad := range []string{
		"`{status}`",
		"`{r.get(status)}`",
		"{r.get(status)}",
		"{r.get(file,)}",
		"\\]+",
		"]+",
	} {
		if _, ok := byPath[bad]; ok {
			t.Fatalf("heredoc body token %q must not be surfaced as file change: %#v", bad, targets)
		}
	}
	if len(targets) != 2 {
		t.Fatalf("expected only real redirect targets, got %#v", targets)
	}
}

func TestCollectShellRedirectTargetsSkipsFdTargets(t *testing.T) {
	targets := CollectShellRedirectTargets("printf 'ok\\n' > /dev/null 2>&1")
	if len(targets) != 0 {
		t.Fatalf("fd and /dev/null targets must not be surfaced, got %#v", targets)
	}
}

func joinLines(lines ...string) string {
	out := ""
	for i, line := range lines {
		if i > 0 {
			out += "\n"
		}
		out += line
	}
	return out
}
