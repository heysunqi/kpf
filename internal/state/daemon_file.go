package state

import (
	"encoding/json"
	"errors"
	"os"
	"time"
)

// DaemonInfo is the contents of daemon.json.
type DaemonInfo struct {
	PID       int       `json:"pid"`
	Socket    string    `json:"socket"`
	StartedAt time.Time `json:"started_at"`
	Version   string    `json:"version"`
	LogFile   string    `json:"log_file"`
}

// ReadDaemonFile reads daemon.json. Returns os.ErrNotExist if absent.
func ReadDaemonFile(path string) (DaemonInfo, error) {
	var info DaemonInfo
	data, err := os.ReadFile(path)
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return info, err
	}
	return info, nil
}

// WriteDaemonFile writes daemon.json with 0600 permissions.
func WriteDaemonFile(path string, info DaemonInfo) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// RemoveDaemonFile deletes daemon.json. No-op if already gone.
func RemoveDaemonFile(path string) error {
	err := os.Remove(path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
