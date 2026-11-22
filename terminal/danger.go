package terminal

import (
	"regexp"
	"strings"
)

type dangerRule struct {
	category string
	re       *regexp.Regexp
}

var dangerRules = []dangerRule{
	{
		category: "删除文件",
		re:       regexp.MustCompile(`(?:^|[;&|(\s])rm\s+(?:[^\n;&|]*\s)?(?:-[a-zA-Z]*[rR]|--recursive\b)`),
	},
	{
		category: "提权",
		re:       regexp.MustCompile(`(?:^|[;&|(\s])(?:sudo|su)(?:\s|$)`),
	},
	{
		category: "Git 改写历史",
		re:       regexp.MustCompile(`(?:^|[;&|(\s])git\s+push\b[^\n;&|]*(?:--force-with-lease\b|--force\b|-f\b)`),
	},
	{
		category: "Git 丢弃改动",
		re:       regexp.MustCompile(`(?:^|[;&|(\s])git\s+(?:reset\s+--hard\b|clean\s+[^\n;&|]*-[a-zA-Z]*f|checkout\s+--\s+\.(?:\s|$)|restore\s+\.(?:\s|$))`),
	},
	{
		category: "磁盘",
		re:       regexp.MustCompile(`(?:^|[;&|(\s])(?:mkfs(?:\.[a-zA-Z0-9_-]+)?\b|dd\s+[^\n;&|]*\bif=|diskutil\s+(?:erase\w*|partitionDisk)\b)`),
	},
	{
		category: "权限",
		re:       regexp.MustCompile(`(?:^|[;&|(\s])(?:chmod|chown)\s+(?:[^\n;&|]*\s)?(?:-[a-zA-Z]*R|--recursive\b)`),
	},
	{
		category: "远程脚本",
		re:       regexp.MustCompile(`(?:^|[;&|(\s])(?:curl|wget)\b[^\n|]*\|\s*(?:sh|bash|zsh)\b`),
	},
	{
		category: "系统服务",
		re:       regexp.MustCompile(`(?:^|[;&|(\s])launchctl\s+(?:bootout|unload|remove)\b`),
	},
	{
		category: "进程",
		re:       regexp.MustCompile(`(?:^|[;&|(\s])(?:kill\s+-9\s+1(?:\s|$|;|&|\|)|killall\b|pkill\b)`),
	},
	{
		category: "关机",
		re:       regexp.MustCompile(`(?:^|[;&|(\s])(?:shutdown|reboot|halt)(?:\s|$)`),
	},
	{
		category: "清空文件",
		re:       regexp.MustCompile(`(?:^|[;&|(\s])truncate\b`),
	},
}

var devRedirectRe = regexp.MustCompile(`>\s*(/dev/[^\s;&|]+)`)

// CheckDangerousCommand checks whether cmd matches the v1 high-risk command
// checklist. This is a guardrail against accidental destructive input; the
// real security boundary remains owner-only access.
func CheckDangerousCommand(cmd string) (DangerousMatch, bool) {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return DangerousMatch{}, false
	}

	// Check device writes before generic file truncation so "> /dev/disk..." is
	// classified under "写设备" while "> /dev/null" is ignored.
	for _, m := range devRedirectRe.FindAllStringSubmatch(trimmed, -1) {
		if len(m) > 1 && m[1] != "/dev/null" {
			return DangerousMatch{Category: "写设备", Command: trimmed}, true
		}
	}

	for _, rule := range dangerRules {
		if rule.re.MatchString(trimmed) {
			return DangerousMatch{Category: rule.category, Command: trimmed}, true
		}
	}

	if hasOverwriteRedirect(trimmed) {
		return DangerousMatch{Category: "清空文件", Command: trimmed}, true
	}

	return DangerousMatch{}, false
}

// hasOverwriteRedirect detects unquoted single ">" file truncation (e.g. "> a.txt"
// or "1> a.txt"), ignoring append ">>", fd dup ">&", and "> /dev/null".
func hasOverwriteRedirect(cmd string) bool {
	inSingle := false
	inDouble := false
	escaped := false

	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if inSingle || inDouble || ch != '>' {
			continue
		}
		// Ignore ">>" append or ">&" fd duplication or "<>" read-write.
		if i > 0 && (cmd[i-1] == '>' || cmd[i-1] == '<') {
			continue
		}
		if i+1 < len(cmd) && (cmd[i+1] == '>' || cmd[i+1] == '&') {
			i++
			continue
		}
		rest := strings.TrimSpace(cmd[i+1:])
		if rest == "" {
			continue
		}
		target := strings.FieldsFunc(rest, func(r rune) bool {
			return r == ' ' || r == '\t' || r == ';' || r == '&' || r == '|'
		})
		if len(target) > 0 && target[0] == "/dev/null" {
			continue
		}
		return true
	}
	return false
}
