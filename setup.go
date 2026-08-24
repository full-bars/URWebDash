package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// setup implements `urwebdash setup`. Two modes:
//
//	urwebdash setup                     user-space: token + webhook config
//	sudo urwebdash setup --install-services   root: install systemd units
func runSetup(args []string) {
	installServices := false
	for _, a := range args {
		if a == "--install-services" {
			installServices = true
		}
	}

	if installServices {
		if os.Geteuid() != 0 {
			fatal("--install-services must run as root (use sudo)")
		}
		setupInstallServices()
		return
	}

	ensurePathEntry()
	ensurePathEntry()
	setupToken()
	setupWebhook()

	fmt.Println("\n[setup] complete - starting URWebDash...")
	fmt.Println("(polling runs in the background; press Ctrl+C to stop both)")

	binPath, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(binPath); err == nil {
		binPath = resolved
	}
	fmt.Printf("\nOptional: install system-wide services (runs at boot):\n")
	fmt.Printf("  sudo %s setup --install-services\n", binPath)

	runBoth()
}

// ensurePathEntry makes sure ~/.local/bin is on PATH for future shells by
// appending an export line to the user's shell config. Idempotent: skips if
// the line is already present. Also fixes the CURRENT process env.
func ensurePathEntry() {
	binDir := os.Getenv("HOME") + "/.local/bin"
	if binDir == "/.local/bin" {
		return // no HOME
	}

	// current process
	pathEnv := os.Getenv("PATH")
	if !strings.Contains(pathEnv, binDir) {
		os.Setenv("PATH", pathEnv+":"+binDir)
	}

	home := os.Getenv("HOME")
	if home == "" {
		return
	}

	// candidate rc files by login shell; fall back to common set
	shell := os.Getenv("SHELL")
	var candidates []string
	switch {
	case strings.Contains(shell, "zsh"):
		candidates = []string{".zshrc", ".zprofile"}
	case strings.Contains(shell, "fish"):
		candidates = []string{".config/fish/config.fish"}
	default: // bash and unknown
		candidates = []string{".bashrc", ".profile"}
	}

	for _, rc := range candidates {
		path := filepath.Join(home, filepath.Base(rc))
		if rc == ".config/fish/config.fish" {
			path = filepath.Join(home, ".config/fish/config.fish")
		}
		if b, err := os.ReadFile(path); err == nil &&
			strings.Contains(string(b), ".local/bin") {
			continue // already configured in this rc
		}
		line := "export PATH=$HOME/.local/bin:$PATH\n"
		if strings.Contains(rc, "fish") {
			line = "set -gx PATH $HOME/.local/bin $PATH\n"
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			continue
		}
		f.WriteString("\n# added by urwebdash setup\n" + line)
		f.Close()
		fmt.Printf("[setup] added ~/.local/bin to PATH via %s\n", path)
		return // one rc is enough
	}
}

// runBoth starts the poller in the background and serves the dashboard in
// the foreground until interrupted. Used by `setup` so a fresh install just
// works with no extra commands.
func runBoth() {
	bin, err := os.Executable()
	if err != nil {
		fatal("resolve executable: %v", err)
	}

	poller := exec.Command(bin, "run")
	poller.Stdout = os.Stdout
	poller.Stderr = os.Stderr
	if err := poller.Start(); err != nil {
		fatal("start poller: %v", err)
	}

	serve := exec.Command(bin, "serve")
	serve.Stdout = os.Stdout
	serve.Stderr = os.Stderr
	serve.Stdin = os.Stdin
	if err := serve.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "dashboard exited: %v\n", err)
	}
	// dashboard stopped: take the poller down too so nothing is orphaned
	if poller.Process != nil {
		poller.Process.Signal(syscall.SIGTERM)
		poller.Wait()
	}
	fmt.Println("[setup] stopped.")
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}

// promptTTY returns a reader attached to the controlling terminal.
// Under `curl | bash`, stdin is the pipe - but /dev/tty still reaches the
// user's keyboard, so prompts keep working. Returns nil if truly headless.
func promptReader() *bufio.Reader {
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return bufio.NewReader(os.Stdin)
	}
	if tty, err := os.Open("/dev/tty"); err == nil {
		return bufio.NewReader(tty)
	}
	return nil
}

func prompt(label string) string {
	r := promptReader()
	if r == nil {
		fmt.Printf("%s(skipped: no terminal available)\n", label)
		return ""
	}
	fmt.Print(label)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.TrimSpace(line)
}

// setupToken ensures the JWT file exists, exchanging an auth code if needed.
func setupToken() {
	path := jwtPath()
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		fmt.Printf("[setup] token found at %s\n", path)
		return
	}

	code := os.Getenv("URNETWORK_AUTH_CODE")

	if code == "" {
		fmt.Println("No URnetwork token found at " + path)
		fmt.Println("You can get an auth code at https://ur.io")
		code = prompt("auth code (blank to skip): ")
		if code == "" {
			fmt.Println("[setup] skipped token setup. Add one later either:")
			fmt.Println("  a) copy from your provider machine:")
			fmt.Println("       mkdir -p ~/.urnetwork && scp host:.urnetwork/jwt " + path)
			fmt.Println("  b) run: urwebdash setup   # and paste an auth code")
			os.Exit(1)
		}
	}

	tok, err := exchangeAuthCode(code)
	if err != nil {
		fatal("%v", err)
	}
	if err := writeTokenFile(path, tok); err != nil {
		fatal("write %s: %v", path, err)
	}

	// Clear the auth code from the terminal so it does not linger in
	// scrollback or shell history.
	if r := promptReader(); r != nil {
		clearLine()
	}
	fmt.Printf("[setup] session token saved to %s\n", path)
}

// clearLine erases the current terminal line (best effort, POSIX terminals).
func clearLine() {
	fmt.Print("\033[2K\r")
}

func exchangeAuthCode(code string) (string, error) {
	body, _ := json.Marshal(map[string]string{"auth_code": code})
	resp, err := httpClient.Post("https://api.bringyour.com/auth/code-login",
		"application/json", strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("code-login request failed: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		ByJWT string `json:"by_jwt"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("bad response from code-login: %w", err)
	}
	if out.ByJWT == "" {
		msg := "rejected"
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		return "", fmt.Errorf("auth code %s", msg)
	}
	return out.ByJWT, nil
}

func writeTokenFile(path, tok string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(tok)), 0600); err != nil {
		return err
	}
	cachedJWT = "" // force re-read on next use
	return nil
}

// setupWebhook asks for the Discord webhook and spike threshold interactively.
func setupWebhook() {
	dir := filepath.Dir(jwtPath())
	whPath := filepath.Join(dir, "discord_webhook")
	if b, err := os.ReadFile(whPath); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		fmt.Printf("[setup] webhook already configured (%s)\n", whPath)
		return
	}

	fmt.Println("\nOptional: Discord alerts for payouts and traffic spikes.")
	fmt.Println("Server Settings -> Integrations -> Webhooks -> New Webhook, copy the URL.")
	url := prompt("webhook URL (blank to skip): ")
	if url == "" || (!strings.HasPrefix(url, "https://discord.com/api/webhooks/") &&
		!strings.HasPrefix(url, "discordapp.com/api/webhooks/") &&
		!strings.HasPrefix(url, "https://discordapp.com/api/webhooks/")) {
		if url != "" {
			fmt.Println("[setup] URL does not look like a Discord webhook; skipped")
		}
		return
	}
	os.MkdirAll(dir, 0700)
	os.WriteFile(whPath, []byte(url), 0600)
	fmt.Printf("[setup] webhook saved to %s\n", whPath)

	th := prompt("spike threshold (e.g. 500M, 0.5G, blank for default 1GB): ")
	if th != "" {
		if _, err := parseSize(th); err == nil {
			os.WriteFile(filepath.Join(dir, "spike_threshold"), []byte(th), 0600)
			fmt.Println("[setup] threshold saved")
		} else {
			fmt.Printf("[setup] could not parse %q; using default 1GB\n", th)
		}
	}
}

const unitRunTemplate = `[Unit]
Description=URWebDash polling daemon
Documentation=https://github.com/full-bars/URWebDash
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%s
ExecStart=%s/.local/bin/urwebdash run
Restart=on-failure
RestartSec=30

NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
`

const unitServeTemplate = `[Unit]
Description=URWebDash web dashboard
Documentation=https://github.com/full-bars/URWebDash
After=network-online.target urwebdash-run.service
Wants=network-online.target urwebdash-run.service

[Service]
Type=simple
User=%s
Environment=HOST=127.0.0.1
ExecStart=%s/.local/bin/urwebdash serve 3001
Restart=on-failure
RestartSec=30

NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
`

// setupInstallServices writes units for the invoking user (SUDO_USER when run
// under sudo) and enables them. Root-only by caller contract.
func setupInstallServices() {
	user := os.Getenv("SUDO_USER")
	if user == "" || user == "root" {
		user = "root"
	}
	home, err := homeOf(user)
	if err != nil || home == "" {
		fatal("cannot resolve home for user %q: %v", user, err)
	}
	binPath := filepath.Join(home, ".local", "bin", "urwebdash")
	if _, err := os.Stat(binPath); err != nil {
		fmt.Printf("[setup] warning: binary not found at %s yet - units will point there anyway\n", binPath)
	}

	units := map[string]string{
		"urwebdash-run.service":   fmt.Sprintf(unitRunTemplate, user, home),
		"urwebdash-serve.service": fmt.Sprintf(unitServeTemplate, user, home),
	}
	for name, body := range units {
		path := filepath.Join("/etc/systemd/system", name)
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			fatal("write %s: %v", path, err)
		}
		fmt.Printf("[setup] wrote %s\n", path)
	}

	run := exec.Command("systemctl", "daemon-reload")
	run.Stdout, run.Stderr = os.Stdout, os.Stderr
	if err := run.Run(); err != nil {
		fatal("daemon-reload: %v", err)
	}

	jwt := filepath.Join(home, ".urnetwork", "jwt")
	start := "start"
	if b, err := os.ReadFile(jwt); err != nil || len(strings.TrimSpace(string(b))) == 0 {
		fmt.Println("[setup] no token yet; installing units without starting them")
		start = "enable"
	}
	run = exec.Command("systemctl", start, "urwebdash-run.service", "urwebdash-serve.service")
	run.Stdout, run.Stderr = os.Stdout, os.Stderr
	if err := run.Run(); err != nil {
		fatal("systemctl %s: %v", start, err)
	}
	fmt.Println("[setup] done. dashboard: http://127.0.0.1:3001")
}

func homeOf(user string) (string, error) {
	if user == "root" {
		return "/root", nil
	}
	out, err := exec.Command("getent", "passwd", user).Output()
	if err != nil {
		// fall back to cgo-free lookup
		return "", err
	}
	fields := strings.Split(strings.TrimSpace(string(out)), ":")
	if len(fields) < 6 {
		return "", fmt.Errorf("unexpected getent output")
	}
	return fields[5], nil
}
