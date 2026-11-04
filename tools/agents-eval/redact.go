package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// secretEnvVars are credentials that must never reach a results file. The
// agent's shell cannot see them (CLAUDE_CODE_SUBPROCESS_ENV_SCRUB), but a
// transcript, a project file the agent wrote, or a record could still
// carry one by some path nobody thought of, so every trial directory is
// scanned before anything else reads it.
var secretEnvVars = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN",
	"OPENAI_API_KEY", "GITHUB_TOKEN", "GH_TOKEN",
}

type secret struct{ name, value string }

// secretsFromEnv returns the set secrets worth scanning for. A value under
// 8 bytes is skipped: replacing it would mangle ordinary text.
func secretsFromEnv() []secret {
	var out []secret
	for _, k := range secretEnvVars {
		if v := os.Getenv(k); len(v) >= 8 {
			out = append(out, secret{k, v})
		}
	}
	return out
}

// RedactDir replaces every occurrence of each secret's value in every
// regular file under dir with [REDACTED:<NAME>], and returns the files
// (relative to dir, sorted) where it found one. An empty result means the
// directory was clean.
func RedactDir(dir string, secrets []secret) ([]string, error) {
	if len(secrets) == 0 {
		return nil, nil
	}
	var found []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out, hit := raw, false
		for _, s := range secrets {
			if bytes.Contains(out, []byte(s.value)) {
				hit = true
				out = bytes.ReplaceAll(out, []byte(s.value), []byte("[REDACTED:"+s.name+"]"))
			}
		}
		if !hit {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, out, info.Mode().Perm()); err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		found = append(found, rel)
		return nil
	})
	sort.Strings(found)
	return found, err
}
