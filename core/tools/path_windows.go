//go:build windows

package tools

import "strings"

// platformPath converts Git Bash style paths ("/c/x/y") to Windows paths
// ("C:\x\y").
func platformPath(p string) string {
	if len(p) >= 2 && p[0] == '/' && isLetter(p[1]) && (len(p) == 2 || p[2] == '/') {
		rest := ""
		if len(p) > 2 {
			rest = p[2:]
		}
		p = strings.ToUpper(p[1:2]) + ":" + rest
		if rest == "" {
			p += `\`
		}
	}
	return strings.ReplaceAll(p, "/", `\`)
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
