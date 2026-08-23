package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	setupToken()
	setupWebhook()
	fmt.Println("\nSetup complete. Start it with:")
	fmt.Println("  urwebdash run &      # polling daemon")
	fmt.Println("  urwebdash serve      # dashboard on http://127.0.0.1:3001")
	fmt.Println("\nFor system services (root): sudo urwebdash setup --install-services")
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func prompt(label string) string {
	fmt.Print(label)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return ""
	}
	return strings.TrimSpace(sc.Text())
}

// setupToken ensures the JWT file exists, exchanging an auth code if needed.
func setupToken() {
	path := jwtPath()
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		fmt.Printf("[setup] token found at %s\n", path)
		return
	}

	code := os.Getenv("URNETWORK_AUTH_CODE")
	interactive := stdinIsTTY()

	if code == "" {
		if !interactive {
			fmt.Println(`[setup] no session token and not interactive. Either:
  a) copy one from your provider machine:
       mkdir -p ~/.urnetwork && scp host:.urnetwork/jwt ` + path + `
  b) re-run in a terminal to enter an auth code from https://ur.io`)
			os.Exit(1)
		}
		fmt.Println("No URnetwork token found at " + path)
		fmt.Println("Get an auth code at https://ur.io, then paste it here.")
		code = prompt("auth code (blank to skip): ")
		if code == "" {
			fmt.Println("[setup] skipped token setup; the dashboard needs one to start.")
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
	fmt.Printf("[setup] session token saved to %s\n", path)
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
	if !stdinIsTTY() {
		fmt.Println("[setup] non-interactive: skipped webhook config")
		return
	}
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
