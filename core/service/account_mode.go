package service

import (
	"strings"

	jserver "github.com/df-mc/dragonfly/server/java/protocol/server"
)

// How the Java listener checks a login with Mojang.
const (
	javaAuthAll     = "all"           // every login: encryption + session-server hasJoined
	javaAuthPremium = "premium-names" // only names of premium accounts (hybrid), the rest offline
	javaAuthNone    = "none"          // nobody: anyone under any name
)

// accountSettings is the Java account mode resolved from the options.
type accountSettings struct {
	Mode     string // "online", "hybrid" or "offline" (account_mode, for the log)
	JavaAuth string // javaAuthAll / javaAuthPremium / javaAuthNone
	Hook     bool   // OnlineIdentity (derived stable XUID via the plugin hook) vs OfflineIdentity
	Unknown  string // an account_mode value that was not understood (treated as online)
}

// resolveAccountMode reads account_mode (and the older online_mode / java_online / identity_hook
// options):
//
//	account_mode = "online"  (default) every Java login verified with Mojang, derived XUID
//	account_mode = "hybrid"  premium names verified as online, other names offline (cracked);
//	                         both get a derived XUID (premium: from the verified UUID, cracked:
//	                         from the offline UUID)
//	account_mode = "offline" no verification, no XUID
//
// java_online overrides only the listener: "true" = everyone verified, "false" or "hybrid" =
// premium names verified (hybrid), "unverified" = nobody verified (tests only). identity_hook
// overrides the identity provider.
func resolveAccountMode(opt func(string) string) accountSettings {
	mode := strings.ToLower(strings.TrimSpace(opt("account_mode")))
	if mode == "" {
		// Back-compat: the old online_mode option, else the premium default.
		if v := strings.TrimSpace(opt("online_mode")); v != "" {
			if strings.EqualFold(v, "true") {
				mode = "online"
			} else {
				mode = "offline"
			}
		} else {
			mode = "online"
		}
	}
	var s accountSettings
	switch mode {
	case "offline":
		s = accountSettings{Mode: mode, JavaAuth: javaAuthNone}
	case "hybrid":
		s = accountSettings{Mode: mode, JavaAuth: javaAuthPremium, Hook: true}
	case "online":
		s = accountSettings{Mode: mode, JavaAuth: javaAuthAll, Hook: true}
	default:
		s = accountSettings{Mode: "online", JavaAuth: javaAuthAll, Hook: true, Unknown: mode}
	}
	switch strings.ToLower(strings.TrimSpace(opt("java_online"))) {
	case "":
	case "true":
		s.JavaAuth = javaAuthAll
	case "unverified":
		s.JavaAuth = javaAuthNone
	default: // "false", "hybrid": premium names keep their identity (owner's rule)
		s.JavaAuth = javaAuthPremium
	}
	if v := strings.TrimSpace(opt("identity_hook")); v != "" {
		s.Hook = strings.EqualFold(v, "true")
	}
	return s
}

// applyJavaAuth sets the listener's Mojang checks for s. names is used for javaAuthPremium.
// Behind Velocity modern forwarding the proxy authenticates and none of this applies.
func applyJavaAuth(cfg *jserver.Config, s accountSettings, names *premiumNames) {
	if len(cfg.VelocitySecret) > 0 {
		return
	}
	switch s.JavaAuth {
	case javaAuthAll:
		cfg.OnlineMode = true
	case javaAuthPremium:
		cfg.OnlineModeFor = names.RequiresLogin
		cfg.AuthFailedMessage = hybridRefusal
	}
}
