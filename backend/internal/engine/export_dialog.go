package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/umbraforge/desktop/internal/procutil"
)

// ChooseSavePath opens a native "Save As" dialog and returns the chosen path.
// cancel returns empty path and nil error.
func ChooseSavePath(title, defaultName string) (string, error) {
	if strings.TrimSpace(defaultName) == "" {
		defaultName = "umbraforge-accounts.json"
	}
	if strings.TrimSpace(title) == "" {
		title = "导出账号"
	}
	switch runtime.GOOS {
	case "darwin":
		// osascript: choose file name
		script := fmt.Sprintf(`try
  set theFile to choose file name with prompt %q default name %q
  return POSIX path of theFile
on error number -128
  return ""
end try`, title, defaultName)
		out, err := procutil.Command("osascript", "-e", script).CombinedOutput()
		if err != nil {
			// user cancel often still exit 0 with empty; other errors
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				return "", nil
			}
			return "", fmt.Errorf("save dialog: %s", msg)
		}
		path := strings.TrimSpace(string(out))
		return path, nil
	case "windows":
		// PowerShell SaveFileDialog
		ps := fmt.Sprintf(`
Add-Type -AssemblyName System.Windows.Forms
$d = New-Object System.Windows.Forms.SaveFileDialog
$d.Title = '%s'
$d.FileName = '%s'
$d.Filter = 'All files (*.*)|*.*'
if ($d.ShowDialog() -eq 'OK') { $d.FileName } else { '' }
`, escapePS(title), escapePS(defaultName))
		out, err := procutil.Command("powershell", "-NoProfile", "-Command", ps).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("save dialog: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	default:
		// Linux: zenity or kdialog
		if _, err := exec.LookPath("zenity"); err == nil {
			out, err := procutil.Command("zenity", "--file-selection", "--save", "--confirm-overwrite",
				"--title="+title, "--filename="+defaultName).CombinedOutput()
			if err != nil {
				// cancel
				return "", nil
			}
			return strings.TrimSpace(string(out)), nil
		}
		if _, err := exec.LookPath("kdialog"); err == nil {
			out, err := procutil.Command("kdialog", "--getsavefilename", defaultName).CombinedOutput()
			if err != nil {
				return "", nil
			}
			return strings.TrimSpace(string(out)), nil
		}
		// fallback home
		home, _ := os.UserHomeDir()
		return filepath.Join(home, defaultName), nil
	}
}

func escapePS(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// WriteExportFile writes content to path (creates dirs if needed).
func WriteExportFile(path, content string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("empty path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}
