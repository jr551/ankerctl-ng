package handler

import (
	"os"
	"path/filepath"
	"runtime"
)

// slicerLoginCandidates returns platform-specific paths where the eufyMake /
// AnkerMake slicer stores its login cache. The caller checks each path for
// existence and readability.
//
// Mirrors web/platform.py _login_candidates() — Linux returns no candidates
// because there is no known local slicer login path for that OS.
func slicerLoginCandidates() []string {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		return []string{
			filepath.Join(home, "Library", "Application Support",
				"AnkerMake", "AnkerMake_64bit_fp", "login.json"),
		}

	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		appData := os.Getenv("APPDATA")

		var candidates []string

		// LevelDB files used by newer eufyMake Studio (checked first, newest first).
		if localAppData != "" {
			leveldbDir := filepath.Join(localAppData,
				"eufyMake Studio Profile", "EBWebView", "Default",
				"Local Storage", "leveldb")
			if entries, err := os.ReadDir(leveldbDir); err == nil {
				// Collect .ldb and .log files in reverse-sorted order (newest first).
				var ldbs []string
				for _, e := range entries {
					name := e.Name()
					ext := filepath.Ext(name)
					if ext == ".ldb" || ext == ".log" {
						ldbs = append(ldbs, filepath.Join(leveldbDir, name))
					}
				}
				// Reverse sort (os.ReadDir returns alphabetical order).
				for i, j := 0, len(ldbs)-1; i < j; i, j = i+1, j-1 {
					ldbs[i], ldbs[j] = ldbs[j], ldbs[i]
				}
				candidates = append(candidates, ldbs...)
			}
		}

		// Legacy / fallback paths.
		if appData != "" {
			candidates = append(candidates,
				filepath.Join(appData, "eufyMake Studio Profile", "cache", "offline", "user_info"),
			)
		}
		if localAppData != "" {
			candidates = append(candidates,
				filepath.Join(localAppData, "Ankermake", "AnkerMake_64bit_fp", "login.json"),
				filepath.Join(localAppData, "Ankermake", "login.json"),
			)
		}
		return candidates

	default:
		// Linux and other OSes: no known slicer login cache path.
		return nil
	}
}
