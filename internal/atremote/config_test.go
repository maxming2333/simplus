package atremote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bridges.json")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	// Chmod explicitly so the assertion does not depend on the process umask.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("set configuration mode: %v", err)
	}
	return path
}

const validConfig = `{"bridges":[{"key":"esp32-a","baseUrl":"http://192.0.2.10","profile":"ml307a","username":"agent","password":"secret","requestTimeoutMs":15000}]}`

func TestLoadConfigAcceptsPrivateBoundedFile(t *testing.T) {
	config, err := LoadConfig(writeConfig(t, validConfig, 0o600))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(config.Bridges) != 1 {
		t.Fatalf("bridges = %d, want 1", len(config.Bridges))
	}
	bridge := config.Bridges[0]
	if bridge.Target.Key != "esp32-a" || bridge.Profile != "ml307a" || bridge.AttestCapabilities {
		t.Fatalf("bridge = %+v", bridge)
	}
	if bridge.Target.RequestTimeout != 15*time.Second {
		t.Fatalf("request timeout = %s", bridge.Target.RequestTimeout)
	}
	if !bridge.Target.Plaintext() || bridge.Target.BaseURLHost() != "192.0.2.10" {
		t.Fatalf("target host = %q plaintext = %v", bridge.Target.BaseURLHost(), bridge.Target.Plaintext())
	}
	if targets := config.Targets(); len(targets) != 1 || targets[0].Key != "esp32-a" {
		t.Fatalf("targets = %+v", targets)
	}
}

func TestLoadConfigDefaultsRequestTimeout(t *testing.T) {
	config, err := LoadConfig(writeConfig(t, `{"bridges":[{"key":"a","baseUrl":"https://bridge.invalid/api","profile":"ml307a"}]}`, 0o400))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	target := config.Bridges[0].Target
	if target.RequestTimeout != defaultRequestTimeout {
		t.Fatalf("request timeout = %s, want %s", target.RequestTimeout, defaultRequestTimeout)
	}
	if target.Plaintext() {
		t.Fatal("https target reported as plaintext")
	}
	if target.baseURL != "https://bridge.invalid/api" {
		t.Fatalf("base URL = %q", target.baseURL)
	}
}

func TestLoadConfigTrimsTrailingBaseURLSlash(t *testing.T) {
	config, err := LoadConfig(writeConfig(t, `{"bridges":[{"key":"a","baseUrl":"http://192.0.2.10/at-bridge/","profile":"ml307a"}]}`, 0o600))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := config.Bridges[0].Target.baseURL; got != "http://192.0.2.10/at-bridge" {
		t.Fatalf("base URL = %q", got)
	}
}

func TestLoadConfigRejectsInvalidDefinitions(t *testing.T) {
	for _, testCase := range []struct{ name, body string }{
		{name: "no bridges", body: `{"bridges":[]}`},
		{name: "missing bridges", body: `{}`},
		{name: "unknown field", body: `{"bridges":[{"key":"a","baseUrl":"http://192.0.2.10","profile":"ml307a","mqttTopic":"x"}]}`},
		{name: "trailing object", body: validConfig + `{"bridges":[]}`},
		{name: "invalid key", body: `{"bridges":[{"key":"Bad_Key","baseUrl":"http://192.0.2.10","profile":"ml307a"}]}`},
		{name: "duplicate key", body: `{"bridges":[{"key":"a","baseUrl":"http://192.0.2.10","profile":"ml307a"},{"key":"a","baseUrl":"http://192.0.2.11","profile":"ml307a"}]}`},
		{name: "invalid profile", body: `{"bridges":[{"key":"a","baseUrl":"http://192.0.2.10","profile":"ML307A"}]}`},
		{name: "missing profile", body: `{"bridges":[{"key":"a","baseUrl":"http://192.0.2.10"}]}`},
		{name: "unsupported scheme", body: `{"bridges":[{"key":"a","baseUrl":"mqtt://192.0.2.10","profile":"ml307a"}]}`},
		{name: "no host", body: `{"bridges":[{"key":"a","baseUrl":"http:///at","profile":"ml307a"}]}`},
		{name: "userinfo", body: `{"bridges":[{"key":"a","baseUrl":"http://agent:secret@192.0.2.10","profile":"ml307a"}]}`},
		{name: "query", body: `{"bridges":[{"key":"a","baseUrl":"http://192.0.2.10/at?token=x","profile":"ml307a"}]}`},
		{name: "fragment", body: `{"bridges":[{"key":"a","baseUrl":"http://192.0.2.10/at#frag","profile":"ml307a"}]}`},
		{name: "username without password", body: `{"bridges":[{"key":"a","baseUrl":"http://192.0.2.10","profile":"ml307a","username":"agent"}]}`},
		{name: "password without username", body: `{"bridges":[{"key":"a","baseUrl":"http://192.0.2.10","profile":"ml307a","password":"secret"}]}`},
		{name: "username with colon", body: `{"bridges":[{"key":"a","baseUrl":"http://192.0.2.10","profile":"ml307a","username":"a:b","password":"c"}]}`},
		{name: "negative timeout", body: `{"bridges":[{"key":"a","baseUrl":"http://192.0.2.10","profile":"ml307a","requestTimeoutMs":-1}]}`},
		{name: "timeout below minimum", body: `{"bridges":[{"key":"a","baseUrl":"http://192.0.2.10","profile":"ml307a","requestTimeoutMs":10}]}`},
		{name: "timeout above maximum", body: `{"bridges":[{"key":"a","baseUrl":"http://192.0.2.10","profile":"ml307a","requestTimeoutMs":600000}]}`},
		{name: "not json", body: `bridges`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := LoadConfig(writeConfig(t, testCase.body, 0o600)); err == nil {
				t.Fatal("LoadConfig accepted an invalid configuration")
			}
		})
	}
}

func TestLoadConfigRejectsUnsafeFiles(t *testing.T) {
	t.Run("group readable", func(t *testing.T) {
		if _, err := LoadConfig(writeConfig(t, validConfig, 0o640)); err == nil {
			t.Fatal("LoadConfig accepted a group-readable configuration")
		}
	})
	t.Run("world readable", func(t *testing.T) {
		if _, err := LoadConfig(writeConfig(t, validConfig, 0o604)); err == nil {
			t.Fatal("LoadConfig accepted a world-readable configuration")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		target := writeConfig(t, validConfig, 0o600)
		link := filepath.Join(t.TempDir(), "link.json")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		if _, err := LoadConfig(link); err == nil {
			t.Fatal("LoadConfig accepted a symlinked configuration")
		}
	})
	t.Run("directory", func(t *testing.T) {
		if _, err := LoadConfig(t.TempDir()); err == nil {
			t.Fatal("LoadConfig accepted a directory")
		}
	})
	t.Run("relative path", func(t *testing.T) {
		if _, err := LoadConfig("bridges.json"); err == nil {
			t.Fatal("LoadConfig accepted a relative path")
		}
	})
	t.Run("uncleaned path", func(t *testing.T) {
		path := writeConfig(t, validConfig, 0o600)
		if _, err := LoadConfig(filepath.Dir(path) + "/./" + filepath.Base(path)); err == nil {
			t.Fatal("LoadConfig accepted an uncleaned path")
		}
	})
	t.Run("missing", func(t *testing.T) {
		if _, err := LoadConfig(filepath.Join(t.TempDir(), "absent.json")); err == nil {
			t.Fatal("LoadConfig accepted a missing file")
		}
	})
	t.Run("oversize", func(t *testing.T) {
		body := `{"bridges":[{"key":"a","baseUrl":"http://192.0.2.10","profile":"` + strings.Repeat("x", maximumConfigSize) + `"}]}`
		if _, err := LoadConfig(writeConfig(t, body, 0o600)); err == nil {
			t.Fatal("LoadConfig accepted an oversize file")
		}
	})
}

func TestLoadConfigErrorsWithholdCredentials(t *testing.T) {
	_, err := LoadConfig(writeConfig(t, `{"bridges":[{"key":"a","baseUrl":"http://192.0.2.10","profile":"ml307a","username":"agent","password":"top-secret","requestTimeoutMs":5}]}`, 0o600))
	if err == nil {
		t.Fatal("LoadConfig accepted an invalid timeout")
	}
	for _, secret := range []string{"top-secret", "192.0.2.10"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("configuration error leaked %q: %v", secret, err)
		}
	}
}

func TestNewTargetRejectsUnbuiltTargetsInOpener(t *testing.T) {
	if _, err := NewOpener([]Target{{Key: "a"}}); err == nil {
		t.Fatal("NewOpener accepted a target that NewTarget did not build")
	}
	if _, err := NewOpener(nil); err == nil {
		t.Fatal("NewOpener accepted an empty target list")
	}
	target, err := NewTarget("a", "http://192.0.2.10", "", "", 0)
	if err != nil {
		t.Fatalf("NewTarget: %v", err)
	}
	if _, err := NewOpener([]Target{target, target}); err == nil {
		t.Fatal("NewOpener accepted duplicate bridge keys")
	}
}
