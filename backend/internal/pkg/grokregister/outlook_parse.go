package grokregister

import (
	"strings"
)

// ParseOutlookData parses lines: email----password----client_id----refresh_token
func ParseOutlookData(data, proxyURL string) []EmailService {
	var accounts []EmailService
	for lineNum, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "----")
		if len(parts) < 4 {
			Logf("[GrokRegister] outlook line %d bad format, skip", lineNum+1)
			continue
		}
		accounts = append(accounts, NewOutlookEmail(
			strings.TrimSpace(parts[0]),
			strings.TrimSpace(parts[1]),
			strings.TrimSpace(parts[2]),
			strings.TrimSpace(parts[3]),
			proxyURL,
		))
	}
	return accounts
}
