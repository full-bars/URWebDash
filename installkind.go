package main

import (
	"os"
	"runtime"
	"strings"
)

type installProbe struct {
	goos     string
	getenv   func(string) string
	stat     func(string) error
	readFile func(string) ([]byte, error)
}

func (p installProbe) kind() string {
	if p.getenv("URWEBDASH_INSTALL") == "docker" {
		return "docker"
	}
	if err := p.stat("/.dockerenv"); err == nil {
		return "docker"
	}
	if data, err := p.readFile("/proc/1/cgroup"); err == nil {
		for _, marker := range []string{"docker", "containerd", "kubepods"} {
			if strings.Contains(string(data), marker) {
				return "docker"
			}
		}
	}
	if p.goos == "linux" && p.getenv("INVOCATION_ID") != "" {
		return "systemd"
	}
	return "binary"
}

func installKind() string {
	return (installProbe{goos: runtime.GOOS, getenv: os.Getenv, stat: func(path string) error { _, err := os.Stat(path); return err }, readFile: os.ReadFile}).kind()
}
