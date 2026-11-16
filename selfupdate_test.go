package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSelfUpdateChecksums(t *testing.T) {
	hash := strings.Repeat("a", 64)
	asset := "urwebdash-linux-amd64.tar.gz"
	for _, tc := range []struct {
		name, data string
		ok         bool
	}{{"good", hash + "  " + asset, true}, {"single", hash + " " + asset, true}, {"binary", hash + " *" + asset, true}, {"missing", hash + "  other", false}, {"malformed", "oops  " + asset, false}, {"duplicate", hash + "  " + asset + "\n" + hash + "  " + asset, false}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := checksumFor([]byte(tc.data), asset)
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
func TestSelfUpdatePlatform(t *testing.T) {
	for _, osname := range []string{"linux", "darwin"} {
		for _, arch := range []string{"amd64", "arm64"} {
			a, e := assetName(osname, arch)
			if e != nil || a != "urwebdash-"+osname+"-"+arch+".tar.gz" {
				t.Fatalf("%s %v", a, e)
			}
		}
	}
	if _, err := assetName("windows", "amd64"); err == nil {
		t.Fatal("unsupported accepted")
	}
}
func testArchive(t *testing.T, headers []*tar.Header, contents [][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for i, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if len(contents[i]) > 0 {
			if _, err := tw.Write(contents[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestSelfUpdateTarSafety(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    []*tar.Header
		data [][]byte
		ok   bool
	}{
		{"good", []*tar.Header{{Name: "urwebdash", Mode: 0755, Size: 1}}, [][]byte{[]byte("a")}, true},
		{"traversal", []*tar.Header{{Name: "../urwebdash", Mode: 0755}}, [][]byte{nil}, false},
		{"wrong", []*tar.Header{{Name: "other", Mode: 0755}}, [][]byte{nil}, false},
		{"symlink", []*tar.Header{{Name: "urwebdash", Typeflag: tar.TypeSymlink, Linkname: "x"}}, [][]byte{nil}, false},
		{"hardlink", []*tar.Header{{Name: "urwebdash", Typeflag: tar.TypeLink, Linkname: "x"}}, [][]byte{nil}, false},
		{"multiple", []*tar.Header{{Name: "urwebdash"}, {Name: "other"}}, [][]byte{nil, nil}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := unpackUpdate(testArchive(t, tc.h, tc.data))
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v", err)
			}
		})
	}
	t.Run("oversize", func(t *testing.T) {
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(&tar.Header{Name: "urwebdash", Size: selfUpdateLimit + 1, Mode: 0755}); err != nil {
			t.Fatal(err)
		}
		gz.Flush()
		gz.Close()
		if _, err := unpackUpdate(b.Bytes()); err == nil {
			t.Fatal("oversize accepted")
		}
	})
}
func TestSelfUpdateDownloadLimit(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "123456") }))
	defer s.Close()
	if _, _, err := downloadFile(s.Client(), s.URL, 5); err == nil {
		t.Fatal("oversized response accepted")
	}
	if b, _, err := downloadFile(s.Client(), s.URL, 6); err != nil || string(b) != "123456" {
		t.Fatalf("%q %v", b, err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestSelfUpdateActualDownloadLimits(t *testing.T) {
	for _, limit := range []int64{checksumLimit, selfUpdateLimit} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.CopyN(w, zeroReader{}, limit+1)
		}))
		_, _, err := downloadFile(s.Client(), s.URL, limit)
		s.Close()
		if err == nil {
			t.Fatalf("accepted response larger than %d", limit)
		}
	}
}
func TestSelfUpdateMatchingSystemUnits(t *testing.T) {
	exe := "/home/example/.local/bin/urwebdash"
	read := func(path string) ([]byte, error) {
		return []byte("[Service]\nExecStart=" + exe + " run\n"), nil
	}
	if !systemUnitsMatch(exe, read) {
		t.Fatal("matching units not found")
	}
	if systemUnitsMatch("/other/urwebdash", read) {
		t.Fatal("unrelated executable matched")
	}
	if systemUnitsMatch(exe, func(string) ([]byte, error) { return []byte("# ExecStart=" + exe + " run\n"), nil }) {
		t.Fatal("comment matched")
	}
	if systemUnitsMatch(exe, func(string) ([]byte, error) { return nil, os.ErrNotExist }) {
		t.Fatal("missing unit matched")
	}
}
func TestSelfUpdateSystemdCommand(t *testing.T) {
	for _, tc := range []struct {
		root bool
		name string
		args string
	}{{true, "systemctl", "restart urwebdash-run.service urwebdash-serve.service"}, {false, "sudo", "-n systemctl restart urwebdash-run.service urwebdash-serve.service"}} {
		n, a := systemdCommand(tc.root, "restart", updateUnits...)
		if n != tc.name || strings.Join(a, " ") != tc.args {
			t.Fatalf("%s %v", n, a)
		}
	}
}
func selfUpdateFixture(t *testing.T, corrupt, missing bool) (selfUpdateConfig, *bytes.Buffer, string) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh missing")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "urwebdash")
	if err := os.WriteFile(exe, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	script := []byte("#!/bin/sh\n[ \"$1\" = version ] && echo v1.0.1\n")
	data := testArchive(t, []*tar.Header{{Name: "urwebdash", Mode: 0755, Size: int64(len(script))}}, [][]byte{script})
	sum := sha256.Sum256(data)
	asset, _ := assetName(runtime.GOOS, runtime.GOARCH)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api":
			fmt.Fprint(w, `{"tag_name":"v1.0.1","html_url":"https://github.com/full-bars/URWebDash/releases/tag/v1.0.1"}`)
		case strings.HasSuffix(r.URL.Path, "/SHA256SUMS"):
			if missing {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, "%x  %s\n", sum, asset)
		case strings.HasSuffix(r.URL.Path, "/"+asset):
			if corrupt {
				w.Write([]byte("bad"))
				return
			}
			w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	var output bytes.Buffer
	return selfUpdateConfig{version: "v1.0.0", kind: "binary", goos: runtime.GOOS, arch: runtime.GOARCH, exe: exe, endpoint: s.URL + "/api", base: s.URL, client: s.Client(), runner: func(context.Context, string, ...string) error { return nil }, wait: time.Second, output: &output}, &output, exe
}
func TestSelfUpdateEndToEnd(t *testing.T) {
	c, out, exe := selfUpdateFixture(t, false, false)
	if rc := selfUpdate(nil, c); rc != 0 {
		t.Fatalf("rc=%d %s", rc, out)
	}
	b, _ := os.ReadFile(exe)
	if !bytes.Contains(b, []byte("v1.0.1")) {
		t.Fatal("not replaced")
	}
	old, err := os.ReadFile(exe + ".pre-v1.0.0")
	if err != nil || string(old) != "old" {
		t.Fatalf("backup %q %v", old, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), ".urwebdash.new")); !os.IsNotExist(err) {
		t.Fatal("candidate remains")
	}
}
func TestSelfUpdateBackupExistsCleansCandidate(t *testing.T) {
	c, out, exe := selfUpdateFixture(t, false, false)
	backup := exe + ".pre-v1.0.0"
	if err := os.WriteFile(backup, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if selfUpdate(nil, c) != 0 {
		t.Fatalf("existing backup blocked update: %s", out)
	}
	old, _ := os.ReadFile(exe)
	kept, _ := os.ReadFile(backup)
	if !bytes.Contains(old, []byte("v1.0.1")) || string(kept) != "old" {
		t.Fatalf("binary=%q backup=%q", old, kept)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), ".urwebdash.new")); !os.IsNotExist(err) {
		t.Fatal("candidate remains")
	}
}
func TestSelfUpdateMismatchAndMissing(t *testing.T) {
	for _, missing := range []bool{false, true} {
		c, out, exe := selfUpdateFixture(t, !missing, missing)
		if rc := selfUpdate(nil, c); rc != 1 {
			t.Fatalf("rc=%d %s", rc, out)
		}
		b, _ := os.ReadFile(exe)
		if string(b) != "old" {
			t.Fatal("binary changed")
		}
		if missing && !strings.Contains(out.String(), installCommand) {
			t.Fatal("installer missing")
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(exe), ".urwebdash.new")); !os.IsNotExist(err) {
			t.Fatal("candidate remains")
		}
	}
}
func TestSelfUpdateRefusalsAndCheck(t *testing.T) {
	c, out, _ := selfUpdateFixture(t, false, false)
	c.kind = "docker"
	if selfUpdate(nil, c) != 2 || !strings.Contains(out.String(), "docker pull ghcr.io/full-bars/urwebdash:latest") || !strings.Contains(out.String(), "docker compose pull && docker compose up -d") {
		t.Fatalf("%s", out)
	}
	c.kind = "binary"
	c.version = "dev"
	if selfUpdate(nil, c) != 2 {
		t.Fatal("dev accepted")
	}
	c.version = "v1.0.0"
	c.goos = "windows"
	if selfUpdate(nil, c) != 2 {
		t.Fatal("platform accepted")
	}
	c.goos = runtime.GOOS
	if selfUpdate([]string{"--check"}, c) != 10 {
		t.Fatal("check update")
	}
	c.version = "v1.0.1"
	if selfUpdate([]string{"--check"}, c) != 0 || selfUpdate(nil, c) != 0 {
		t.Fatal("latest")
	}
	c.endpoint = "http://127.0.0.1:1"
	if selfUpdate([]string{"--check"}, c) != 1 {
		t.Fatal("check error")
	}
}
func TestSelfUpdateRollback(t *testing.T) {
	c, out, exe := selfUpdateFixture(t, false, false)
	c.kind = "systemd"
	c.wait = time.Millisecond
	restarts := 0
	c.runner = func(_ context.Context, _ string, args ...string) error {
		for _, a := range args {
			if a == "restart" {
				restarts++
				return nil
			}
		}
		return fmt.Errorf("inactive")
	}
	if selfUpdate(nil, c) != 1 {
		t.Fatalf("unexpected success: %s", out)
	}
	b, _ := os.ReadFile(exe)
	if string(b) != "old" || restarts != 2 {
		t.Fatalf("binary=%q restarts=%d %s", b, restarts, out)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), ".urwebdash.new")); !os.IsNotExist(err) {
		t.Fatal("candidate remains")
	}
}
func TestSelfUpdateSelfTestFailure(t *testing.T) {
	d := t.TempDir()
	_, err := writeCandidate(d, []byte("#!/bin/sh\necho wrong\n"), "v1.0.1")
	if err == nil {
		t.Fatal("wrong version accepted")
	}
	entries, readErr := os.ReadDir(d)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("failed candidate left files: %v %v", entries, readErr)
	}
}
func TestRetryCleansOnlyOldCandidates(t *testing.T) {
	c, out, exe := selfUpdateFixture(t, false, false)
	dir := filepath.Dir(exe)
	for _, name := range []string{".urwebdash.new", ".urwebdash.new-stale", ".urwebdash.new-fresh", "unrelated"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		if name != ".urwebdash.new-fresh" {
			old := time.Now().Add(-time.Hour)
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Symlink(filepath.Join(dir, "unrelated"), filepath.Join(dir, ".urwebdash.new-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".urwebdash.new-dir"), 0700); err != nil {
		t.Fatal(err)
	}
	if rc := selfUpdate(nil, c); rc != 0 {
		t.Fatalf("rc=%d %s", rc, out)
	}
	for _, name := range []string{".urwebdash.new", ".urwebdash.new-stale"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s remains: %v", name, err)
		}
	}
	for _, name := range []string{".urwebdash.new-fresh", ".urwebdash.new-link", ".urwebdash.new-dir", "unrelated"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s removed: %v", name, err)
		}
	}
}

func TestRetryBackupAtomic(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "backup")
	if err := os.WriteFile(src, []byte("new"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		for {
			select {
			case <-done:
				errs <- nil
				return
			default:
			}
			b, err := os.ReadFile(dst)
			if err != nil {
				errs <- err
				return
			}
			if string(b) != "old" && string(b) != "new" {
				errs <- fmt.Errorf("partial backup %q", b)
				return
			}
		}
	}()
	for i := 0; i < 100; i++ {
		if err := copyBackup(src, dst); err != nil {
			close(done)
			<-errs
			t.Fatal(err)
		}
	}
	close(done)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

func TestRetryPermissionHint(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not constrain root")
	}
	c, out, exe := selfUpdateFixture(t, false, false)
	dir := filepath.Dir(exe)
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0755)
	if rc := selfUpdate(nil, c); rc != 1 {
		t.Fatalf("rc=%d %s", rc, out)
	}
	if !strings.Contains(out.String(), "permissions") || !strings.Contains(out.String(), installCommand) {
		t.Fatalf("missing permission hint: %s", out)
	}
}

func TestRetryHonestNonPermissionError(t *testing.T) {
	c, out, exe := selfUpdateFixture(t, false, false)
	if err := os.Mkdir(exe+".pre-v1.0.0", 0700); err != nil {
		t.Fatal(err)
	}
	if rc := selfUpdate(nil, c); rc != 1 {
		t.Fatalf("rc=%d %s", rc, out)
	}
	if strings.Contains(out.String(), "permissions") || (!strings.Contains(out.String(), "file exists") && !strings.Contains(out.String(), "is a directory")) {
		t.Fatalf("dishonest error: %s", out)
	}
}

func TestRetryRollbackWithLeftover(t *testing.T) {
	c, out, exe := selfUpdateFixture(t, false, false)
	c.kind = "systemd"
	calls := 0
	c.runner = func(_ context.Context, _ string, _ ...string) error {
		calls++
		if calls == 1 {
			if err := os.WriteFile(filepath.Join(filepath.Dir(exe), ".urwebdash.new"), []byte("leftover"), 0600); err != nil {
				t.Fatal(err)
			}
			return fmt.Errorf("restart failed")
		}
		return nil
	}
	if rc := selfUpdate(nil, c); rc != 1 {
		t.Fatalf("rc=%d %s", rc, out)
	}
	b, err := os.ReadFile(exe)
	if err != nil || string(b) != "old" || strings.Contains(out.String(), "ROLLBACK FAILED") {
		t.Fatalf("binary=%q err=%v %s", b, err, out)
	}
}

func TestSelfUpdateReleaseChecksums(t *testing.T) {
	b, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	release := strings.Split(strings.Split(s, "  release:")[1], "  docker:")[0]
	if !strings.Contains(release, "sha256sum urwebdash-*.tar.gz > SHA256SUMS") || !strings.Contains(release, "\n            SHA256SUMS\n") {
		t.Fatal("release checksums missing")
	}
}
