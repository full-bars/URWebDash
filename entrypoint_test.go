package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The docker entrypoint must never write to the host jwt mount and must
// always prefer an existing /data/jwt. These checks pin the ordering rules
// in docker/entrypoint.sh so refactors cannot silently regress them.

const entrypointScript = "docker/entrypoint.sh"

func readEntrypoint(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(entrypointScript)
	if err != nil {
		t.Fatalf("read %s: %v", entrypointScript, err)
	}
	return string(b)
}

// TestEntrypoint_JWTPreferenceOrder guards the resolution order:
// existing /data/jwt beats /host-jwt, which beats URNETWORK_AUTH_CODE.
func TestEntrypoint_JWTPreferenceOrder(t *testing.T) {
	s := readEntrypoint(t)

	// Search only executable lines (skip the doc comment which mentions all three).
	lines := strings.Split(s, "\n")
	var iExisting, iHost, iAuthCode = -1, -1, -1
	for idx, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		if iExisting == -1 && strings.Contains(ln, `[ -s "$JWT" ]`) {
			iExisting = idx
		}
		if iHost == -1 && strings.Contains(ln, `-s /host-jwt`) {
			iHost = idx
		}
		if iAuthCode == -1 && strings.Contains(ln, `URNETWORK_AUTH_CODE:-`) {
			iAuthCode = idx
		}
	}

	if iExisting == -1 || iHost == -1 || iAuthCode == -1 {
		t.Fatalf("entrypoint missing one of the JWT source branches")
	}
	if !(iExisting < iHost && iHost < iAuthCode) {
		t.Errorf("JWT branch order wrong: existing(%d) must precede host-mount(%d) must precede auth-code(%d)",
			iExisting, iHost, iAuthCode)
	}
}

// TestEntrypoint_NoHostJWTWrite ensures the script never redirects output
// to /host-jwt - it is a read-only mount by contract.
func TestEntrypoint_NoHostJWTWrite(t *testing.T) {
	s := readEntrypoint(t)
	for _, bad := range []string{"> /host-jwt", ">> /host-jwt", "cp /host-jwt /host-jwt"} {
		if strings.Contains(s, bad) {
			t.Errorf("entrypoint writes to /host-jwt via %q", bad)
		}
	}
	if !strings.Contains(s, `cp /host-jwt "$JWT"`) {
		t.Errorf("host jwt copy should target $JWT (the data volume), got no 'cp /host-jwt' line")
	}
}

// TestEntrypoint_WgetNoHSTS pins the --no-hsts flag so no .wget-hsts cache
// file leaks into the user's data volume.
func TestEntrypoint_WgetNoHSTS(t *testing.T) {
	s := readEntrypoint(t)
	wgetLines := []string{}
	for _, ln := range strings.Split(s, "\n") {
		if strings.Contains(ln, "wget ") {
			wgetLines = append(wgetLines, ln)
		}
	}
	if len(wgetLines) == 0 {
		t.Fatal("no wget invocation found in entrypoint")
	}
	for _, ln := range wgetLines {
		if !strings.Contains(ln, "--no-hsts") {
			t.Errorf("wget call missing --no-hsts: %s", ln)
		}
	}
}

// TestEntrypoint_RootDropPrivileges ensures root startup chowns /data and
// re-execs as PUID/PGID rather than staying root.
func TestEntrypoint_RootDropPrivileges(t *testing.T) {
	s := readEntrypoint(t)
	if !strings.Contains(s, `chown -R`) {
		t.Error("root path must chown /data so bind mounts created by docker are writable")
	}
	if !strings.Contains(s, "su-exec") {
		t.Error("root path must drop privileges via su-exec")
	}
	if !strings.Contains(s, "PUID:-1000") {
		t.Error("PUID/PGID defaults should be 1000")
	}
}

// TestParseSize_RegressionCoversDockerDocs keeps the size grammar aligned
// with what example.env documents.
func TestParseSize_RegressionCoversDockerDocs(t *testing.T) {
	docs := map[string]int64{
		"500MB": 500 * 1024 * 1024,
		"500m":  500 * 1024 * 1024,
		"0.5G":  512 * 1024 * 1024,
		"1.5GB": 1536 * 1024 * 1024,
	}
	for in, want := range docs {
		got, err := parseSize(in)
		if err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	// plain bytes with no unit
	if n, _ := parseSize("1048576"); n != 1048576 {
		t.Errorf("plain-bytes parse broken: %d", n)
	}
	// fractional under one unit rounds down to zero without error
	if n, err := parseSize("0.0001K"); err != nil || n != 0 {
		t.Errorf("tiny fraction: n=%d err=%v", n, err)
	}
}

// TestSpikeThresholdFileFallback covers reading SPIKE_THRESHOLD from the
// ~/.urnetwork/spike_threshold file when the env var is unset.
func TestSpikeThresholdFileFallback(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)        // linux
	t.Setenv("USERPROFILE", tmpHome) // windows
	t.Setenv("SPIKE_THRESHOLD", "")

	dir := filepath.Join(tmpHome, ".urnetwork")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "spike_threshold")
	if err := os.WriteFile(path, []byte("250M\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if got := spikeThreshold(); got != 250*1024*1024 {
		t.Errorf("spikeThreshold() from file = %d, want %d", got, 250*1024*1024)
	}

	// bad content falls back to the default rather than erroring
	os.WriteFile(path, []byte("garbage"), 0600)
	if got := spikeThreshold(); got != 1_000_000_000 {
		t.Errorf("garbage file: spikeThreshold() = %d, want default", got)
	}
}

// TestSetupWebhookValidation pins the Discord webhook prefix check in setup.go.
func TestSetupWebhookValidation(t *testing.T) {
	b, err := os.ReadFile("setup.go")
	if err != nil {
		t.Skip("setup.go not present")
	}
	s := string(b)
	for _, need := range []string{
		"discord.com/api/webhooks/",
		"discordapp.com/api/webhooks/",
	} {
		if !strings.Contains(s, need) {
			t.Errorf("setup webhook validation missing %q", need)
		}
	}
}

func TestExtractByJWT(t *testing.T) {
	// valid response
	input := `{"by_jwt":"aa.bb.cc","error":null}`
	r, w, _ := os.Pipe()
	oldStdin := os.Stdin
	os.Stdin = r
	w.WriteString(input)
	w.Close()

	tmp := *os.Stdout
	devnull, _ := os.Open(os.DevNull)
	os.Stdout = devnull
	defer func() { os.Stdin = oldStdin; os.Stdout = &tmp; devnull.Close() }()

	extractByJWT() // exits 1 on failure; reaching here means success
}

func TestExtractByJWT_RejectsMissing(t *testing.T) {
	if os.Getenv("BE_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=TestExtractByJWT_RejectsMissing")
		cmd.Env = append(os.Environ(), "BE_CHILD=1")
		err := cmd.Run()
		exitErr, ok := err.(*exec.ExitError)
		if !ok || exitErr.ExitCode() == 0 {
			t.Fatal("expected non-zero exit for missing by_jwt")
		}
		return
	}
	r, w, _ := os.Pipe()
	oldStdin := os.Stdin
	os.Stdin = r
	w.WriteString(`{"error":{"message":"bad code"}}`)
	w.Close()
	defer func() { os.Stdin = oldStdin }()
	extractByJWT() // should os.Exit(1)
	t.Fatal("extractByJWT should have exited")
}
