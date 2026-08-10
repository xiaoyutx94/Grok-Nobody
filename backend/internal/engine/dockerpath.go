package engine

import (
	"github.com/umbraforge/desktop/internal/procutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var (
	dockerPathOnce   sync.Once
	dockerPathCached string
)

// dockerBin returns an absolute path to docker if possible.
// GUI apps on macOS often lack /opt/homebrew/bin in PATH, so inspect looks "missing".
func dockerBin() string {
	dockerPathOnce.Do(func() {
		if p, err := exec.LookPath("docker"); err == nil && p != "" {
			dockerPathCached = p
			return
		}
		cands := []string{
			"/opt/homebrew/bin/docker",
			"/usr/local/bin/docker",
			"/Applications/Docker.app/Contents/Resources/bin/docker",
			"/usr/bin/docker",
		}
		if runtime.GOOS == "windows" {
			cands = []string{
				`C:\Program Files\Docker\Docker\resources\bin\docker.exe`,
				`C:\Program Files\Docker\Docker\resources\docker.exe`,
			}
			if local := os.Getenv("LOCALAPPDATA"); local != "" {
				cands = append(cands, filepath.Join(local, "Docker", "docker.exe"))
			}
		}
		// also scan PATH entries that may not be active in GUI
		for _, dir := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
			if dir == "" {
				continue
			}
			cands = append(cands, filepath.Join(dir, "docker"))
			if runtime.GOOS == "windows" {
				cands = append(cands, filepath.Join(dir, "docker.exe"))
			}
		}
		for _, c := range cands {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				dockerPathCached = c
				return
			}
		}
		dockerPathCached = "docker" // last resort
	})
	return dockerPathCached
}

func dockerCmd(args ...string) *exec.Cmd {
	return procutil.Command(dockerBin(), args...)
}

func dockerCmdContext(ctx interface{ Done() <-chan struct{} }, args ...string) *exec.Cmd {
	// keep signature simple — callers with context use CommandContext separately
	return procutil.Command(dockerBin(), args...)
}
