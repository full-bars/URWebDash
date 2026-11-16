package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseBody(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	script, err := filepath.Abs("scripts/release-body.sh")
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, string, int) {
		t.Helper()
		cmd := exec.Command(bash, append([]string{script}, args...)...)
		cmd.Dir = t.TempDir()
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if err == nil {
			return stdout.String(), stderr.String(), 0
		}
		if exit, ok := err.(*exec.ExitError); ok {
			return stdout.String(), stderr.String(), exit.ExitCode()
		}
		t.Fatal(err)
		return "", "", -1
	}
	if _, stderr, code := run("v0.0.25"); code != 0 {
		t.Fatalf("release body script exit %d: %s", code, stderr)
	}
	quick, err := os.ReadFile("releases/QUICK_INSTALL.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ tag, want, not string }{
		{"v0.0.25", "This release adds a small trend graph", "The server stopped counting unpaid bytes"},
		{"v0.0.24", "The server stopped counting unpaid bytes", "This release adds a small trend graph"},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			out, stderr, code := run(tc.tag)
			if code != 0 || stderr != "" {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
			for _, text := range []string{"## Quick install\n", "curl -fsSL https://raw.githubusercontent.com/full-bars/URWebDash/master/install.sh | bash", "docker run -d --name urwebdash", tc.want} {
				if !strings.Contains(out, text) {
					t.Errorf("missing %q", text)
				}
			}
			if !strings.HasPrefix(out, string(quick)+"\n") || strings.Count(out, "## Quick install") != 1 || strings.Contains(out, tc.not) || strings.Contains(out, "# "+tc.tag+"\n") {
				t.Errorf("wrong release body: %q", out)
			}
		})
	}
	t.Run("unknown", func(t *testing.T) {
		out, stderr, code := run("v9.9.9")
		if code != 0 || out != string(quick) || stderr != "::warning::no release notes section for v9.9.9\n" {
			t.Errorf("exit %d, stdout %q, stderr %q", code, out, stderr)
		}
	})
	t.Run("empty section", func(t *testing.T) {
		root := t.TempDir()
		for _, dir := range []string{"scripts", "releases"} {
			if err := os.Mkdir(filepath.Join(root, dir), 0755); err != nil {
				t.Fatal(err)
			}
		}
		source, err := os.ReadFile(script)
		if err != nil {
			t.Fatal(err)
		}
		for name, contents := range map[string][]byte{
			"scripts/release-body.sh":   source,
			"releases/QUICK_INSTALL.md": quick,
			"releases/RELEASE_NOTES.md": []byte("# v1\n \n	\n# v2\nother\n"),
		} {
			if err := os.WriteFile(filepath.Join(root, name), contents, 0644); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command(bash, filepath.Join(root, "scripts/release-body.sh"), "v1")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil || stdout.String() != string(quick) || stderr.String() != "::warning::no release notes section for v1\n" {
			t.Errorf("error %v, stdout %q, stderr %q", err, stdout.String(), stderr.String())
		}
	})
	t.Run("missing argument", func(t *testing.T) {
		out, stderr, code := run()
		if code != 2 || out != "" || !strings.Contains(stderr, "Usage:") {
			t.Errorf("exit %d, stdout %q, stderr %q", code, out, stderr)
		}
	})
}

func TestReleaseWorkflowBody(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(data)
	start, end := strings.Index(workflow, "\n  release:\n"), strings.Index(workflow, "\n  docker:\n")
	if start < 0 || end <= start {
		t.Fatal("release job not found")
	}
	job := workflow[start:end]
	for _, text := range []string{`body_path: release-body.md`, `bash scripts/release-body.sh "$GITHUB_REF_NAME" > release-body.md`} {
		if !strings.Contains(job, text) {
			t.Errorf("release job missing %q", text)
		}
	}
}
