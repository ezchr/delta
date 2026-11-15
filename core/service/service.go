// Package service turns the host's JSON configuration into the running core.
package service

import (
	"encoding/json"
	"fmt"

	"javabridge/core/host"
)

// Config is the JSON the plugin passes to jb_start. Unknown fields are ignored, so the plugin
// may send more than a given core version reads.
type Config struct {
	// Mode is "bridge" (default) or "spike" (the c-shared feasibility test).
	Mode string `json:"mode"`
	// Listen is the Java listener address, e.g. "0.0.0.0:25565".
	Listen string `json:"listen"`
	// Bedrock is the server's own Bedrock address over loopback, e.g. "127.0.0.1:19132".
	Bedrock string `json:"bedrock"`
	// DataDir is a directory the core may write to (caches, logs).
	DataDir string `json:"data_dir"`
	// Debug turns on debug log lines.
	Debug bool `json:"debug"`
	// MOTD is the Java server list text.
	MOTD string `json:"motd"`
	// MaxPlayers is shown in the Java server list.
	MaxPlayers int `json:"max_players"`
	// Options are passed through to the bridge (see core/bridge/config.go).
	Options map[string]string `json:"options"`
}

// bridgeStarter is set by the bridge package's init, so this package does not import it in
// spike-only builds and tests.
var bridgeStarter func(Config, *host.Logs) (host.Service, error)

func init() {
	host.Register(func(raw json.RawMessage, logs *host.Logs) (host.Service, error) {
		cfg := Config{Mode: "bridge", Listen: "0.0.0.0:25565", Bedrock: "127.0.0.1:19132", MaxPlayers: 20}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &cfg); err != nil {
				return nil, fmt.Errorf("config: %w", err)
			}
		}
		logs.SetDebug(cfg.Debug)
		switch cfg.Mode {
		case "spike":
			return startSpike(cfg, logs)
		case "bridge", "":
			if bridgeStarter == nil {
				return nil, fmt.Errorf("this build has no bridge")
			}
			return bridgeStarter(cfg, logs)
		}
		return nil, fmt.Errorf("config: unknown mode %q", cfg.Mode)
	})
}
