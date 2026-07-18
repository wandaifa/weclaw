package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const launchAgentLabel = "ai.weclaw.bridge"

var (
	launchdStatePattern = regexp.MustCompile(`(?m)^\s*state = (\S+)\s*$`)
	launchdPIDPattern   = regexp.MustCompile(`(?m)^\s*pid = (\d+)\s*$`)
)

func launchAgentPlist() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
}

func launchAgentDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func launchAgentTarget() string {
	return launchAgentDomain() + "/" + launchAgentLabel
}

func launchAgentInstalled() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	info, err := os.Stat(launchAgentPlist())
	return err == nil && !info.IsDir()
}

func launchAgentStatus() (string, int, bool) {
	out, err := exec.Command("launchctl", "print", launchAgentTarget()).Output()
	if err != nil {
		return "", 0, false
	}
	state, pid := parseLaunchAgentStatus(string(out))
	return state, pid, true
}

func parseLaunchAgentStatus(output string) (string, int) {
	state := ""
	if match := launchdStatePattern.FindStringSubmatch(output); len(match) == 2 {
		state = strings.TrimSpace(match[1])
	}
	pid := 0
	if match := launchdPIDPattern.FindStringSubmatch(output); len(match) == 2 {
		pid, _ = strconv.Atoi(match[1])
	}
	return state, pid
}

func startLaunchAgent() error {
	state, pid, loaded := launchAgentStatus()
	if loaded {
		fmt.Printf("weclaw is managed by launchd (state=%s", state)
		if pid > 0 {
			fmt.Printf(", pid=%d", pid)
		}
		fmt.Println(")")
		return nil
	}
	if err := exec.Command("launchctl", "bootstrap", launchAgentDomain(), launchAgentPlist()).Run(); err != nil {
		return fmt.Errorf("start launchd service: %w", err)
	}
	fmt.Println("weclaw started by launchd")
	return nil
}

func restartLaunchAgent() error {
	if _, _, loaded := launchAgentStatus(); loaded {
		if err := exec.Command("launchctl", "kickstart", "-k", launchAgentTarget()).Run(); err != nil {
			return fmt.Errorf("restart launchd service: %w", err)
		}
	} else if err := exec.Command("launchctl", "bootstrap", launchAgentDomain(), launchAgentPlist()).Run(); err != nil {
		return fmt.Errorf("start launchd service: %w", err)
	}
	fmt.Println("weclaw restarted by launchd")
	return nil
}

func stopLaunchAgent() error {
	if _, _, loaded := launchAgentStatus(); !loaded {
		fmt.Println("weclaw launchd service is already stopped")
		return nil
	}
	if err := exec.Command("launchctl", "bootout", launchAgentDomain(), launchAgentPlist()).Run(); err != nil {
		return fmt.Errorf("stop launchd service: %w", err)
	}
	fmt.Println("weclaw stopped by launchd")
	return nil
}

func printLaunchAgentStatus() {
	state, pid, loaded := launchAgentStatus()
	if !loaded {
		fmt.Println("weclaw is managed by launchd but is not loaded")
		return
	}
	fmt.Printf("weclaw is managed by launchd (state=%s", state)
	if pid > 0 {
		fmt.Printf(", pid=%d", pid)
	}
	fmt.Println(")")
	fmt.Printf("Log: %s\n", filepath.Join(filepath.Dir(launchAgentPlist()), "..", "Logs", "weclaw", "launchd.err.log"))
}
