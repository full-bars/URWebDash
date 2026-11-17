package main

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestInstallKind(t *testing.T) {
	for _, tt := range []struct {
		name, goos, install, invocation, cgroup, want string
		dockerenv                                     bool
	}{
		{name: "env override", goos: "linux", install: "docker", want: "docker"},
		{name: "dockerenv", goos: "linux", dockerenv: true, want: "docker"},
		{name: "cgroup docker", goos: "linux", cgroup: "0::/docker/abc", want: "docker"},
		{name: "cgroup containerd", goos: "linux", cgroup: "0::/containerd/abc", want: "docker"},
		{name: "cgroup kubepods", goos: "linux", cgroup: "0::/kubepods/abc", want: "docker"},
		{name: "systemd", goos: "linux", invocation: "abc", want: "systemd"},
		{name: "plain fallback", goos: "linux", want: "binary"},
		{name: "empty invocation", goos: "linux", invocation: "", want: "binary"},
		{name: "docker beats systemd", goos: "linux", dockerenv: true, invocation: "abc", want: "docker"},
		{name: "darwin invocation", goos: "darwin", invocation: "abc", want: "binary"},
		{name: "non docker env", goos: "linux", install: "Docker", want: "binary"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			probe := installProbe{
				goos: tt.goos,
				getenv: func(key string) string {
					switch key {
					case "URWEBDASH_INSTALL":
						return tt.install
					case "INVOCATION_ID":
						return tt.invocation
					}
					return ""
				},
				stat: func(path string) error {
					if path != "/.dockerenv" {
						t.Errorf("unexpected stat path %q", path)
					}
					if tt.dockerenv {
						return nil
					}
					return errors.New("not found")
				},
				readFile: func(path string) ([]byte, error) {
					switch path {
					case "/proc/1/cgroup":
						if tt.cgroup != "" {
							return []byte(tt.cgroup), nil
						}
					default:
						t.Errorf("unexpected path %q", path)
					}
					return nil, errors.New("not found")
				},
			}
			if got := probe.kind(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUpdateInstallJSON(t *testing.T) {
	t.Setenv("URWEBDASH_INSTALL", "docker")
	for _, tt := range []struct {
		name, version      string
		enabled, available bool
	}{
		{"available", "v0.0.25", true, true},
		{"disabled", "v0.0.25", false, false},
		{"dev", "dev", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := newUpdateChecker(tt.version, tt.enabled, "")
			c.result.Available = tt.available
			rr := httptest.NewRecorder()
			c.handle(rr, httptest.NewRequest("GET", "/api/update", nil))
			var got map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got["install"] != "docker" {
				t.Errorf("install = %v, want docker; body %s", got["install"], rr.Body.String())
			}
			if got["available"] != tt.available {
				t.Errorf("available = %v, want %v", got["available"], tt.available)
			}
		})
	}
}

func TestDockerfileInstallMarker(t *testing.T) {
	data, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "URWEBDASH_INSTALL=docker") {
		t.Error("missing URWEBDASH_INSTALL=docker")
	}
}
