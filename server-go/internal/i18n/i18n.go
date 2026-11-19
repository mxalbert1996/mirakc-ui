// Package i18n is the port of server/locales/i18n.ts: the server-side
// display strings (ntfy/Discord notification text), resolved with a
// dotted-key t() that mirrors i18next behavior.
//
// The Deno server loads a TS module (server/locales/ja-JP); this port
// embeds the same strings as a Go map literal (see ja_jp.go). The strings
// are copied verbatim. The two can drift only while the frozen Deno server
// still runs; the rewrite plan accepted that risk until cutover.
package i18n

import (
	"regexp"
	"strings"
)

// {{name}} placeholders, same \w+ shape as the Deno t().
var placeholderRe = regexp.MustCompile(`\{\{(\w+)\}\}`)

// T resolves a dotted key against the embedded ja-JP server strings and
// interpolates {{var}}; an unknown key or missing var resolves to the
// literal key / placeholder, exactly like the Deno t().
func T(key string, vars map[string]string) string {
	var node any = translation
	for _, segment := range strings.Split(key, ".") {
		children, ok := node.(map[string]any)
		if !ok {
			return key
		}
		node, ok = children[segment]
		if !ok {
			return key
		}
	}
	value, ok := node.(string)
	if !ok {
		return key
	}
	return placeholderRe.ReplaceAllStringFunc(value, func(placeholder string) string {
		if v, ok := vars[placeholder[2:len(placeholder)-2]]; ok {
			return v
		}
		return placeholder
	})
}
