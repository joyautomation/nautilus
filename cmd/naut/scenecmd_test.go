package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sceneTestTypes = `TYPE
  Tank : STRUCT Level : REAL; TempC : REAL; END_STRUCT;
  Motor : STRUCT Running : BOOL; Fault : BOOL; Speed : REAL; END_STRUCT;
END_TYPE
`

const sceneTestProgram = `PROGRAM Main
VAR_EXTERNAL T101 : Tank; P101 : Motor; Demand : REAL; END_VAR
P101.Running := T101.Level < Demand;
END_PROGRAM
`

const sceneTestManifest = `name: scenetest
tasks:
  - program: main.st
tags:
  - { name: T101, role: state, type: Tank }
  - { name: P101, role: state, type: Motor }
  - { name: Demand, role: setpoint, init: 50.0 }
`

func sceneProject(t *testing.T, extra map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{"nautilus.yaml": sceneTestManifest, "types.st": sceneTestTypes, "main.st": sceneTestProgram}
	for k, v := range extra {
		files[k] = v
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSceneInitGeneratesACheckedScene(t *testing.T) {
	dir := sceneProject(t, nil)
	out := captureStdoutOf(t, func() int { return runScene([]string{"init", dir}) })
	if !strings.Contains(out, "wrote") || !strings.Contains(out, "2 nodes (pump ×1, tank ×1)") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	path := filepath.Join(dir, "scenetest.scene.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"tag": "P101"`) || !strings.Contains(string(data), `"kind": "tank"`) {
		t.Fatalf("generated:\n%s", data)
	}
	// naut check now sees the file and reports it clean.
	out, code := checkIn(t, map[string]string{"nautilus.yaml": sceneTestManifest, "types.st": sceneTestTypes, "main.st": sceneTestProgram, "scenetest.scene.json": string(data)})
	if code != 0 || !strings.Contains(out, "scene scenetest.scene.json: 2 nodes, 0 pipes") {
		t.Fatalf("check code %d:\n%s", code, out)
	}
	// A second run refuses to overwrite.
	if code := runScene([]string{"init", dir}); code == 0 {
		t.Fatal("a second init should refuse without --force")
	}
}

func TestCheckHoldsASceneToTheTags(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": sceneTestManifest, "types.st": sceneTestTypes, "main.st": sceneTestProgram,
		"plant.scene.json": `{"nodes": [
			{"id": "a", "kind": "tank", "tag": "P101", "pos": [0, 0, 0]},
			{"id": "b", "kind": "pump", "tag": "P999", "pos": [1, 0, 0]},
			{"id": "c", "kind": "rack", "tag": "T101", "pos": [2, 0, 0]}
		]}`,
	})
	if code == 0 {
		t.Fatalf("want a failing check:\n%s", out)
	}
	for _, want := range []string{
		"error: scene plant.scene.json /nodes/0/tag:",
		"kinds.tank.type",
		"warning: scene plant.scene.json /nodes/1/tag: the manifest declares no tag \"P999\"",
		"error: scene plant.scene.json /nodes/2/kind: unknown kind \"rack\"",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// captureStdoutOf runs fn with os.Stdout redirected and returns what it printed.
func captureStdoutOf(t *testing.T, fn func() int) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	r.Close()
	return sb.String()
}
