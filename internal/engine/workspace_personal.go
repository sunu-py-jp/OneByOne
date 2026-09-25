package engine

import (
	"os"
	"path/filepath"
	"runtime"
)

// DefaultLocalDirectory is deliberately non-roaming on Windows.
func DefaultLocalDirectory() (string, error) {
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			return filepath.Join(dir, "OneByOne"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "AppData", "Local", "OneByOne"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "OneByOne"), nil
}

func personalDirectory() (string, error) {
	// The explicit override also keeps automated tests and portable installations
	// separate from the signed-in user's settings.
	if dir := os.Getenv("ONEBYONE_PRIVATE_DIR"); dir != "" {
		return filepath.Abs(dir)
	}
	dir, err := DefaultLocalDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "private"), nil
}
