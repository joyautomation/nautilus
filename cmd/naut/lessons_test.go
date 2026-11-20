package main

// Every interactive lesson (website/lessons/<id>/: start/, solution/, check/)
// must be a real exercise: its Check passes on the solution, fails on the
// start (an empty exercise that already passed would teach nothing), and
// start and solution compile. The interactive shell runs the same test files
// in the browser, so a lesson that passes here passes there.

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// copyTree copies src into dst.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLessonsAreRealExercises(t *testing.T) {
	lessons, _ := filepath.Glob("../../website/lessons/*/check")
	if len(lessons) == 0 {
		t.Skip("no lessons yet")
	}
	for _, checkDir := range lessons {
		root := filepath.Dir(checkDir)
		t.Run(filepath.Base(root), func(t *testing.T) {
			try := func(stage string) int {
				dir := t.TempDir()
				copyTree(t, filepath.Join(root, stage), dir)
				copyTree(t, checkDir, dir)
				var code int
				captureStdout(t, func() int { code = runTest([]string{dir}); return code })
				return code
			}
			if code := try("solution"); code != 0 {
				t.Errorf("the Check fails on the solution (exit %d): the lesson cannot be solved", code)
			}
			if code := try("start"); code == 0 {
				t.Errorf("the Check passes on the start project: the exercise is already done")
			}
			for _, stage := range []string{"start", "solution"} {
				dir := t.TempDir()
				copyTree(t, filepath.Join(root, stage), dir)
				var code int
				captureStdout(t, func() int { code = runCheck([]string{dir}); return code })
				if code != 0 {
					t.Errorf("naut check fails on the %s project (exit %d)", stage, code)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "lesson.md")); err != nil {
				t.Errorf("no lesson.md: %v", err)
			}
		})
	}
}
