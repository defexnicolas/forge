package plugins

import (
	"regexp"
	"strings"
)

// userConfigPattern matches references like ${user_config.SOME_KEY} where
// SOME_KEY is alphanumeric + underscore. Strict so we don't accidentally
// rewrite shell-style ${VAR} references the plugin author intended literally.
var userConfigPattern = regexp.MustCompile(`\$\{user_config\.([A-Za-z_][A-Za-z0-9_]*)\}`)

// ExpandVars resolves the two variable forms Claude-compatible plugins use:
//
//   - ${CLAUDE_PLUGIN_ROOT} -> p.Path (the absolute filesystem location of
//     the plugin)
//   - ${user_config.KEY}    -> the value at p.UserConfig[KEY], or empty
//     string when the key is absent
//
// Unrecognized ${...} forms are left untouched so plugin commands that embed
// other shell variables keep working.
func ExpandVars(p Plugin, s string) string {
	if s == "" {
		return s
	}
	out := strings.ReplaceAll(s, "${CLAUDE_PLUGIN_ROOT}", p.Path)
	out = userConfigPattern.ReplaceAllStringFunc(out, func(match string) string {
		sub := userConfigPattern.FindStringSubmatch(match)
		if len(sub) != 2 {
			return match
		}
		if p.UserConfig == nil {
			return ""
		}
		return p.UserConfig[sub[1]]
	})
	return out
}
