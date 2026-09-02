package atremote

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// maximumConfigSize bounds the private configuration file before it is decoded.
const maximumConfigSize = 64 << 10

var profilePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Bridge is one validated remote AT bridge: its transport target plus the
// inventory facts the assembly point needs to synthesize a device report.
type Bridge struct {
	Target  Target
	Profile string

	// AttestCapabilities is an explicit operator attestation, not evidence. It
	// keeps the model adapter's capability statuses for a bridged device even
	// though Simplus has no bounded HIL evidence for that control path. Leaving
	// it false is the fail-closed default: the device stays visible but no
	// business capability, Line or SIM authentication becomes available.
	AttestCapabilities bool
}

// Config is the validated content of the private bridge configuration file.
type Config struct {
	Bridges []Bridge
}

// Targets returns the transport targets for NewOpener.
func (config Config) Targets() []Target {
	targets := make([]Target, 0, len(config.Bridges))
	for _, bridge := range config.Bridges {
		targets = append(targets, bridge.Target)
	}
	return targets
}

type configFile struct {
	Bridges []configBridge `json:"bridges"`
}

type configBridge struct {
	Key                string            `json:"key"`
	BaseURL            string            `json:"baseUrl"`
	Profile            string            `json:"profile"`
	Username           string            `json:"username"`
	Password           string            `json:"password"`
	RequestTimeoutMS   int64             `json:"requestTimeoutMs"`
	CommandTimeoutMS   int64             `json:"commandTimeoutMs"`
	ExchangeTimeoutMS  int64             `json:"exchangeTimeoutMs"`
	Headers            map[string]string `json:"headers"`
	AttestCapabilities bool              `json:"attestCapabilities"`
}

// LoadConfig reads and strictly validates the private bridge configuration.
//
// The file carries bridge credentials, so it must be a private regular file:
// command-line flags and environment values are visible to any local process
// through /proc, and a symlink would let a less privileged path substitute
// content.
func LoadConfig(path string) (Config, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return Config{}, errors.New("remote AT configuration path must be absolute and clean")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Config{}, errors.New("remote AT configuration file is unreadable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Config{}, errors.New("remote AT configuration must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Config{}, errors.New("remote AT configuration must not be group or world accessible")
	}
	if info.Size() > maximumConfigSize {
		return Config{}, errors.New("remote AT configuration exceeds the bounded size")
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, errors.New("remote AT configuration file is unreadable")
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maximumConfigSize+1))
	defer zero(body)
	if err != nil {
		return Config{}, errors.New("remote AT configuration file is unreadable")
	}
	if len(body) > maximumConfigSize {
		return Config{}, errors.New("remote AT configuration exceeds the bounded size")
	}
	return parseConfig(body)
}

func parseConfig(body []byte) (Config, error) {
	var decoded configFile
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return Config{}, errors.New("remote AT configuration is not valid bounded JSON")
	}
	if decoder.More() {
		return Config{}, errors.New("remote AT configuration must contain exactly one object")
	}
	if len(decoded.Bridges) == 0 {
		return Config{}, errors.New("remote AT configuration must define at least one bridge")
	}
	if len(decoded.Bridges) > 32 {
		return Config{}, errors.New("remote AT configuration defines too many bridges")
	}
	config := Config{Bridges: make([]Bridge, 0, len(decoded.Bridges))}
	seen := make(map[string]bool, len(decoded.Bridges))
	for _, entry := range decoded.Bridges {
		if seen[entry.Key] {
			return Config{}, fmt.Errorf("duplicate remote AT bridge key %q", entry.Key)
		}
		if !profilePattern.MatchString(entry.Profile) {
			return Config{}, fmt.Errorf("remote AT bridge %q has an invalid profile", entry.Key)
		}
		if entry.RequestTimeoutMS < 0 || entry.RequestTimeoutMS > maximumRequestTimeout.Milliseconds() {
			return Config{}, fmt.Errorf("remote AT bridge %q request timeout must be from 1000ms through 120000ms", entry.Key)
		}
		for label, value := range map[string]int64{
			"command timeout": entry.CommandTimeoutMS, "exchange timeout": entry.ExchangeTimeoutMS,
		} {
			if value < 0 || value > maximumBoundedTimeout.Milliseconds() {
				return Config{}, fmt.Errorf("remote AT bridge %q %s must be from 1000ms through 180000ms", entry.Key, label)
			}
		}
		target, err := NewTargetWithOptions(entry.Key, entry.BaseURL, entry.Username, entry.Password, TargetOptions{
			RequestTimeout:  time.Duration(entry.RequestTimeoutMS) * time.Millisecond,
			CommandTimeout:  time.Duration(entry.CommandTimeoutMS) * time.Millisecond,
			ExchangeTimeout: time.Duration(entry.ExchangeTimeoutMS) * time.Millisecond,
			Headers:         entry.Headers,
		})
		if err != nil {
			return Config{}, err
		}
		seen[entry.Key] = true
		config.Bridges = append(config.Bridges, Bridge{
			Target: target, Profile: entry.Profile, AttestCapabilities: entry.AttestCapabilities,
		})
	}
	return config, nil
}
