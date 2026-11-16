package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var selfUpdateBase = "https://github.com/full-bars/URWebDash/releases/download"

const selfUpdateLimit = 64 << 20
const checksumLimit = 1 << 20
const installCommand = "curl -fsSL https://raw.githubusercontent.com/full-bars/URWebDash/master/install.sh | bash"

var checksumLine = regexp.MustCompile(`^([0-9a-fA-F]{64}) (?: |\*)?([^\r\n]+)$`)

func assetName(goos, arch string) (string, error) {
	if (goos != "linux" && goos != "darwin") || (arch != "amd64" && arch != "arm64") {
		return "", errors.New("unsupported platform")
	}
	return "urwebdash-" + goos + "-" + arch + ".tar.gz", nil
}

func checksumFor(data []byte, asset string) ([32]byte, error) {
	var sum [32]byte
	found := false
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		parts := checksumLine.FindStringSubmatch(line)
		if parts == nil {
			return sum, errors.New("malformed checksum line")
		}
		if parts[2] != asset {
			continue
		}
		if found {
			return sum, errors.New("duplicate checksum entry")
		}
		b, err := hex.DecodeString(parts[1])
		if err != nil {
			return sum, err
		}
		copy(sum[:], b)
		found = true
	}
	if !found {
		return sum, errors.New("checksum entry missing")
	}
	return sum, nil
}

func downloadFile(client *http.Client, url string, limit int64) ([]byte, int, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if int64(len(b)) > limit {
		return nil, resp.StatusCode, errors.New("download exceeds size limit")
	}
	return b, resp.StatusCode, nil
}

func unpackUpdate(data []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	h, err := tr.Next()
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	if h.Name != "urwebdash" || (h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA) || h.Size < 0 || h.Size > selfUpdateLimit {
		return nil, errors.New("archive must contain one regular urwebdash file within the size limit")
	}
	b, err := io.ReadAll(io.LimitReader(tr, selfUpdateLimit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != h.Size {
		return nil, errors.New("truncated binary")
	}
	if _, err := tr.Next(); err != io.EOF {
		return nil, errors.New("archive contains extra or invalid members")
	}
	// Bound and validate trailing padding while consuming the gzip trailer.
	padding, err := io.ReadAll(io.LimitReader(gz, selfUpdateLimit+1))
	if err != nil || len(padding) > selfUpdateLimit {
		return nil, errors.New("invalid or oversized archive padding")
	}
	for _, b := range padding {
		if b != 0 {
			return nil, errors.New("unexpected archive data after end marker")
		}
	}
	return b, nil
}

func copyBackup(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(dst), ".urwebdash.backup-*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer os.Remove(tmp)
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Chmod(0755)
	}
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func cleanCandidates(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-10 * time.Minute)
	for _, entry := range entries {
		name := entry.Name()
		if name != ".urwebdash.new" && !strings.HasPrefix(name, ".urwebdash.new-") {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.Mode().IsRegular() && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

func updateFileError(out io.Writer, action string, err error) {
	if errors.Is(err, fs.ErrPermission) {
		fmt.Fprintf(out, "%s (check directory permissions or re-run the installer): %v\n%s\n", action, err, installCommand)
	} else {
		fmt.Fprintf(out, "%s: %v\n", action, err)
	}
}

func writeCandidate(dir string, data []byte, tag string) (string, error) {
	f, err := os.CreateTemp(dir, ".urwebdash.new-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()
	err = f.Chmod(0700)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Chmod(0755)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil || strings.TrimSpace(string(out)) != tag {
		return "", errors.New("new binary failed version self-test")
	}
	keep = true
	return path, nil
}

type updateRunner func(context.Context, string, ...string) error

func systemdCommand(root bool, action string, units ...string) (string, []string) {
	args := append([]string{action}, units...)
	if root {
		return "systemctl", args
	}
	return "sudo", append([]string{"-n", "systemctl"}, args...)
}
func defaultUpdateRunner(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

var updateUnits = []string{"urwebdash-run.service", "urwebdash-serve.service"}

const manualRestart = "sudo systemctl restart urwebdash-run.service urwebdash-serve.service"

// installedSystemUnits identifies units created by setup for this exact binary.
func systemUnitsMatch(exe string, readFile func(string) ([]byte, error)) bool {
	for _, unit := range updateUnits {
		b, err := readFile(filepath.Join("/etc/systemd/system", unit))
		if err != nil {
			return false
		}
		expected := "ExecStart=" + exe + " "
		matched := false
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, expected) {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

type selfUpdateConfig struct {
	version, kind, goos, arch, exe, endpoint, base string
	client                                         *http.Client
	runner                                         updateRunner
	root                                           bool
	wait                                           time.Duration
	output                                         io.Writer
}

func selfUpdate(args []string, c selfUpdateConfig) int {
	out := c.output
	if len(args) > 1 || (len(args) == 1 && args[0] != "--check") {
		fmt.Fprintln(out, "Usage: urwebdash update [--check]")
		return 1
	}
	check := len(args) == 1
	if c.kind == "docker" {
		fmt.Fprintln(out, "Docker installs cannot update the binary. Pull the image and re-create the container:")
		fmt.Fprintln(out, "docker pull ghcr.io/full-bars/urwebdash:latest")
		fmt.Fprintln(out, "docker compose pull && docker compose up -d")
		return 2
	}
	current, ok := parseUpdateVersion(c.version)
	if !ok {
		fmt.Fprintln(out, "Cannot update a development or invalid version; use the installer.")
		return 2
	}
	asset, err := assetName(c.goos, c.arch)
	if err != nil {
		fmt.Fprintln(out, "Cannot update: unsupported platform.")
		return 2
	}
	var exe string
	if !check {
		exe, err = filepath.EvalSymlinks(c.exe)
		if err != nil {
			fmt.Fprintf(out, "Resolve executable: %v\n", err)
			return 1
		}
		cleanCandidates(filepath.Dir(exe))
	}
	rel, err := fetchUpdate(c.client, c.endpoint)
	if err != nil {
		fmt.Fprintf(out, "Release check failed: %v\n", err)
		return 1
	}
	latest, _ := parseUpdateVersion(rel.Tag)
	if compareUpdateVersion(latest, current) <= 0 {
		fmt.Fprintf(out, "Already up to date: %s (latest %s).\n", c.version, rel.Tag)
		return 0
	}
	if check {
		fmt.Fprintf(out, "Update available: %s -> %s\n", c.version, rel.Tag)
		return 10
	}
	base := strings.TrimRight(c.base, "/") + "/" + rel.Tag + "/"
	sums, status, err := downloadFile(c.client, base+"SHA256SUMS", checksumLimit)
	if err != nil {
		if status == http.StatusNotFound {
			fmt.Fprintf(out, "This release has no checksum file. Use the install command:\n%s\n", installCommand)
		} else {
			fmt.Fprintf(out, "Checksum download failed: %v\n", err)
		}
		return 1
	}
	sum, err := checksumFor(sums, asset)
	if err != nil {
		fmt.Fprintf(out, "Invalid checksum file: %v\n", err)
		return 1
	}
	data, _, err := downloadFile(c.client, base+asset, selfUpdateLimit)
	if err != nil {
		fmt.Fprintf(out, "Download failed: %v\n", err)
		return 1
	}
	if sha256.Sum256(data) != sum {
		fmt.Fprintln(out, "Checksum mismatch; binary unchanged.")
		return 1
	}
	binary, err := unpackUpdate(data)
	if err != nil {
		fmt.Fprintf(out, "Invalid archive: %v\n", err)
		return 1
	}
	candidate, err := writeCandidate(filepath.Dir(exe), binary, rel.Tag)
	if err != nil {
		updateFileError(out, "Could not prepare update", err)
		return 1
	}
	defer os.Remove(candidate)
	backup := exe + ".pre-" + c.version
	if err = copyBackup(exe, backup); err != nil {
		updateFileError(out, "Could not back up binary", err)
		return 1
	}
	if err = os.Rename(candidate, exe); err != nil {
		updateFileError(out, "Could not replace binary", err)
		return 1
	}
	if c.kind == "systemd" {
		ctx, cancel := context.WithTimeout(context.Background(), c.wait)
		defer cancel()
		name, argv := systemdCommand(c.root, "restart", updateUnits...)
		err = c.runner(ctx, name, argv...)
		if err == nil {
			for {
				active := true
				for _, unit := range updateUnits {
					n, a := systemdCommand(c.root, "is-active", "--quiet", unit)
					if c.runner(ctx, n, a...) != nil {
						active = false
					}
				}
				if active {
					break
				}
				select {
				case <-ctx.Done():
					err = ctx.Err()
				case <-time.After(250 * time.Millisecond):
				}
				if err != nil {
					break
				}
			}
		}
		if err != nil {
			fmt.Fprintf(out, "Restart failed; restoring old binary. If needed, run:\n%s\n", manualRestart)
			if e := copyBackup(backup, exe); e != nil {
				fmt.Fprintf(out, "ROLLBACK FAILED: %v; restore %s manually.\n", e, backup)
			}
			rctx, rcancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer rcancel()
			if e := c.runner(rctx, name, argv...); e != nil {
				fmt.Fprintf(out, "Could not restart old services: %v\n", e)
			}
			fmt.Fprintf(out, "Update failed: %s -> %s; old copy: %s\n", c.version, rel.Tag, backup)
			return 1
		}
		fmt.Fprintf(out, "Updated to %s and restarted both services.\n", rel.Tag)
	} else {
		fmt.Fprintf(out, "Updated to %s. Restart URWebDash to use it. Stop the current session with Ctrl+C, then run urwebdash setup.\n", rel.Tag)
	}
	fmt.Fprintf(out, "Update: %s -> %s; old copy: %s\n", c.version, rel.Tag, backup)
	return 0
}

func runSelfUpdate(args []string) int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	base := selfUpdateBase
	if v := os.Getenv("UPDATE_DOWNLOAD_URL"); v != "" && validUpdateTestURL(v) {
		base = v
	}
	kind := installKind()
	if kind != "docker" && runtime.GOOS == "linux" {
		// INVOCATION_ID also appears in unrelated systemd-launched processes.
		// Restart only units configured to launch this executable.
		resolved, resolveErr := filepath.EvalSymlinks(exe)
		if resolveErr == nil && (systemUnitsMatch(exe, os.ReadFile) || systemUnitsMatch(resolved, os.ReadFile)) {
			kind = "systemd"
		} else {
			kind = "binary"
		}
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	return selfUpdate(args, selfUpdateConfig{version: Version, kind: kind, goos: runtime.GOOS, arch: runtime.GOARCH, exe: exe, endpoint: updateURL(), base: base, client: client, runner: defaultUpdateRunner, root: os.Geteuid() == 0, wait: 15 * time.Second, output: os.Stdout})
}
