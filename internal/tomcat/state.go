package tomcat

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func LoadConfig(stateDir string) (Config, error) {
	var c Config
	if stateDir == "" {
		return c, errors.New("workspace state is unavailable")
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, "tomcat.json"))
	if os.IsNotExist(err) {
		return Config{Context: "/", DebugPort: 5005}, nil
	}
	if err != nil {
		return c, err
	}
	if len(raw) > 16*1024 {
		return c, errors.New("Tomcat config is too large")
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	return c, nil
}
func SaveConfig(stateDir string, c Config) error {
	if stateDir == "" {
		return errors.New("workspace state is unavailable")
	}
	checked, err := ValidateConfig(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(checked, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(stateDir, ".tomcat-config-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(stateDir, "tomcat.json"))
}
