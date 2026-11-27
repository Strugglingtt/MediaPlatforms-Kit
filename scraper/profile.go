package scraper

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// PrepareProfile creates a persistent browser profile directory and reports whether it already contains state.
func PrepareProfile(dir string) bool {
	if dir == "" {
		return false
	}
	_ = os.MkdirAll(dir, 0o755)
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return true
	}
	return false
}

// ReleaseProfile ends residual Chrome processes that use the given browser profile directory.
func ReleaseProfile(dir string) int {
	if dir == "" {
		return 0
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	switch runtime.GOOS {
	case "windows":
		return releaseProfileWindows(dir)
	default:
		killed := releaseProfileUnix(dir)
		clearStaleProfileLocks(dir)
		return killed
	}
}
func clearStaleProfileLocks(dir string) {
	lock := filepath.Join(dir, "SingletonLock")
	target, err := os.Readlink(lock)
	if err != nil {
		return
	}
	i := strings.LastIndexByte(target, '-')
	if i <= 0 {
		return
	}
	host, pidStr := target[:i], target[i+1:]
	pid, pidErr := strconv.Atoi(pidStr)
	stale := pidErr != nil
	if !stale {
		if cur, e := os.Hostname(); e == nil && host != cur {
			stale = true
		} else if !processAlive(pid) {
			stale = true
		}
	}
	if !stale {
		return
	}
	removed := 0
	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
		if err := os.Remove(filepath.Join(dir, name)); err == nil {
			removed++
		}
	}
	if removed > 0 {
		logInfo("[browser] 已清理失效的 profile 锁文件:", lock, "->", target)
	}
}
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return exec.Command("kill", "-0", strconv.Itoa(pid)).Run() == nil
}
func releaseProfileWindows(dir string) int {
	script := `Get-CimInstance Win32_Process -Filter "name='chrome.exe' or name='chromium.exe'" | ` +
		`Where-Object { $_.CommandLine -like '*` + dir + `*' } | ` +
		`ForEach-Object { $_.ProcessId }`
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return 0
	}
	killed := 0
	for _, f := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(f)
		if err != nil || pid <= 0 {
			continue
		}
		if exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run() == nil {
			killed++
			logInfo("[browser] 已结束残留浏览器进程 PID:", pid)
		}
	}
	return killed
}
func releaseProfileUnix(dir string) int {
	out, err := exec.Command("pgrep", "-f", dir).Output()
	if err != nil {
		return 0
	}
	killed := 0
	for _, f := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(f)
		if err != nil || pid <= 0 {
			continue
		}
		if exec.Command("kill", "-9", strconv.Itoa(pid)).Run() == nil {
			killed++
			logInfo("[browser] 已结束残留浏览器进程 PID:", pid)
		}
	}
	return killed
}
