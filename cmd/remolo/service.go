package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
)

// ServiceCmd installs remolo host as a background service for the current user
// (systemd --user on Linux, launchd on macOS), so the host starts at login and
// restarts on failure.
type ServiceCmd struct {
	Install   ServiceInstallCmd   `cmd:"" help:"Install and start the remolo host service"`
	Uninstall ServiceUninstallCmd `cmd:"" help:"Stop and remove the remolo host service"`

	cli.Base
}

// ServiceInstallCmd installs the service.
type ServiceInstallCmd struct {
	Identity string `cli:"identity" help:"Persistent host key path (default ~/.config/remolo/host.key)"`
	Args     string `cli:"args" help:"Extra flags for 'remolo host' (e.g. \"--mdns --once\")"`

	cli.Base
}

func (c *ServiceInstallCmd) Run() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	identity := c.Identity
	if identity == "" {
		home, _ := os.UserHomeDir()
		identity = filepath.Join(home, ".config", "remolo", "host.key")
	}
	args := c.Args
	if args == "" {
		args = "--mdns"
	}
	switch runtime.GOOS {
	case "linux":
		return installSystemdUser(exe, identity, args)
	case "darwin":
		return installLaunchd(exe, identity, args)
	default:
		return fmt.Errorf("service install is not automated on %s yet; run 'remolo host' under your service manager (binary: %s)", runtime.GOOS, exe)
	}
}

func installSystemdUser(exe, identity, args string) error {
	home, _ := os.UserHomeDir()
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	unit := fmt.Sprintf(`[Unit]
Description=remolo host (remote control endpoint)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s host --identity %s %s
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`, exe, identity, args)
	path := filepath.Join(unitDir, "remolo-host.service")
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return err
	}
	fmt.Printf("remolo: wrote %s\n", path)
	run("systemctl", "--user", "daemon-reload")
	if err := run("systemctl", "--user", "enable", "--now", "remolo-host.service"); err != nil {
		fmt.Println("remolo: could not auto-start; run: systemctl --user enable --now remolo-host.service")
		return nil
	}
	fmt.Println("remolo: service enabled and started.")
	fmt.Println("remolo: read the session token with: journalctl --user -u remolo-host -f")
	fmt.Printf("remolo: keep it running after logout with: loginctl enable-linger %s\n", os.Getenv("USER"))
	return nil
}

func installLaunchd(exe, identity, args string) error {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>in.bromb.remolo.host</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>host</string><string>--identity</string><string>%s</string><string>%s</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict></plist>
`, exe, identity, args)
	path := filepath.Join(dir, "in.bromb.remolo.host.plist")
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return err
	}
	fmt.Printf("remolo: wrote %s\n", path)
	run("launchctl", "unload", path)
	if err := run("launchctl", "load", path); err != nil {
		return err
	}
	fmt.Println("remolo: launchd agent loaded and started.")
	return nil
}

// ServiceUninstallCmd removes the service.
type ServiceUninstallCmd struct {
	cli.Base
}

func (c *ServiceUninstallCmd) Run() error {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "linux":
		run("systemctl", "--user", "disable", "--now", "remolo-host.service")
		os.Remove(filepath.Join(home, ".config", "systemd", "user", "remolo-host.service"))
		run("systemctl", "--user", "daemon-reload")
	case "darwin":
		p := filepath.Join(home, "Library", "LaunchAgents", "in.bromb.remolo.host.plist")
		run("launchctl", "unload", p)
		os.Remove(p)
	default:
		return fmt.Errorf("nothing to uninstall on %s", runtime.GOOS)
	}
	fmt.Println("remolo: service removed.")
	return nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
