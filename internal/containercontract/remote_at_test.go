package containercontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const remoteATConfigTarget = "/etc/simplus/remote-at.json"

func readRemoteATOverlay(t *testing.T) composeFile {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repositoryRoot(t), "containers", "compose.remote-at.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var value composeFile
	decoder := yaml.NewDecoder(strings.NewReader(string(body)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode containers/compose.remote-at.yaml: %v", err)
	}
	return value
}

// TestRemoteATOverlayIsAnOptInNetworkWideningAndNothingElse pins the only
// deliberate relaxation of the Agent isolation contract. The overlay may grant
// egress and deliver one private configuration file; it must not become a place
// where capabilities, devices or writable mounts accumulate.
func TestRemoteATOverlayIsAnOptInNetworkWideningAndNothingElse(t *testing.T) {
	overlay := readRemoteATOverlay(t)
	if len(overlay.Services) != 1 {
		t.Fatalf("overlay services = %d, want only agent", len(overlay.Services))
	}
	agent, found := overlay.Services["agent"]
	if !found {
		t.Fatal("overlay must target only the agent service")
	}
	if agent.NetworkMode != "bridge" {
		t.Fatalf("overlay agent network mode = %q, want bridge", agent.NetworkMode)
	}
	if len(agent.Networks) != 0 {
		t.Fatalf("overlay agent networks = %#v; network_mode and networks cannot be combined", agent.Networks)
	}
	if agent.Privileged {
		t.Fatal("overlay agent must not be privileged")
	}
	for name, value := range map[string]int{
		"cap_add": len(agent.CapAdd), "cap_drop": len(agent.CapDrop),
		"device_cgroup_rules": len(agent.DeviceCgroupRules), "ports": len(agent.Ports),
		"security_opt": len(agent.SecurityOpt), "tmpfs": len(agent.Tmpfs),
		"sysctls": len(agent.Sysctls), "group_add": len(agent.GroupAdd),
	} {
		if value != 0 {
			t.Fatalf("overlay agent must not change %s", name)
		}
	}
	if agent.Image != "" || agent.User != "" || len(agent.EntryPoint) != 0 || len(agent.Command) != 0 {
		t.Fatalf("overlay agent must not change its image, user or command: %+v", agent)
	}
	if len(agent.Environment) != 1 || agent.Environment["SIMPLUS_AGENT_REMOTE_AT_CONFIG"] != remoteATConfigTarget {
		t.Fatalf("overlay agent environment = %#v", agent.Environment)
	}
	if len(agent.Volumes) != 1 {
		t.Fatalf("overlay agent volumes = %#v, want exactly one configuration bind", agent.Volumes)
	}
	mount := agent.Volumes[0]
	if mount["type"] != "bind" || mount["target"] != remoteATConfigTarget || mount["read_only"] != true {
		t.Fatalf("overlay configuration mount = %#v", mount)
	}
	bind, ok := mount["bind"].(map[string]any)
	if !ok || bind["create_host_path"] != false {
		t.Fatalf("overlay configuration mount must not create its host path: %#v", mount)
	}
}

// TestRemoteATOverlayDoesNotWeakenTheBaseComposeContract keeps the default
// deployment isolated. The overlay is the only place that grants Agent egress.
func TestRemoteATOverlayDoesNotWeakenTheBaseComposeContract(t *testing.T) {
	base := readCompose(t)
	agent := base.Services["agent"]
	if agent.NetworkMode != "none" || len(agent.Networks) != 0 {
		t.Fatalf("base compose agent network boundary = mode %q networks %#v", agent.NetworkMode, agent.Networks)
	}
	if _, found := agent.Environment["SIMPLUS_AGENT_REMOTE_AT_CONFIG"]; found {
		t.Fatal("base compose must not enable the remote AT bridge path")
	}
	body, err := os.ReadFile(filepath.Join(repositoryRoot(t), "containers", "compose.remote-at.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"network_mode: host", "privileged: true", "cap_add", "device_cgroup_rules"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("overlay contains forbidden privilege contract %q", forbidden)
		}
	}
}
