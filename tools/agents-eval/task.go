package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Task is one entry of the suite: tasks/<id>/ holds
//
//	spec.md           the prompt, verbatim, as a student would get it
//	task.yaml         title, level, required languages
//	hidden_test.yaml  acceptance tests the agent never sees
//	reference/        a known-good solution (scored Good by the unit tests)
type Task struct {
	ID         string   `yaml:"-"`
	Title      string   `yaml:"title"`
	Level      string   `yaml:"level"`
	Languages  []string `yaml:"languages"`
	Spec       string   `yaml:"-"`
	HiddenTest []byte   `yaml:"-"`
	Dir        string   `yaml:"-"`
}

// LoadTasks reads every task under dir, sorted by id. only, when not
// empty, keeps just those ids (and fails on an unknown one).
func LoadTasks(dir string, only []string) ([]*Task, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var tasks []*Task
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t, err := LoadTask(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	if len(only) == 0 {
		return tasks, nil
	}
	byID := map[string]*Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	var out []*Task
	for _, id := range only {
		t, ok := byID[strings.TrimSpace(id)]
		if !ok {
			return nil, fmt.Errorf("unknown task %q", id)
		}
		out = append(out, t)
	}
	return out, nil
}

// LoadTask reads one task directory.
func LoadTask(dir string) (*Task, error) {
	t := &Task{ID: filepath.Base(dir), Dir: dir}
	raw, err := os.ReadFile(filepath.Join(dir, "task.yaml"))
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(raw, t); err != nil {
		return nil, fmt.Errorf("%s: %w", t.ID, err)
	}
	spec, err := os.ReadFile(filepath.Join(dir, "spec.md"))
	if err != nil {
		return nil, err
	}
	t.Spec = strings.TrimSpace(string(spec))
	if t.HiddenTest, err = os.ReadFile(filepath.Join(dir, "hidden_test.yaml")); err != nil {
		return nil, err
	}
	if len(t.Languages) == 0 {
		return nil, fmt.Errorf("%s: task.yaml names no languages", t.ID)
	}
	return t, nil
}
