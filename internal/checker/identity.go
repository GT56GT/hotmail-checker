package checker

import (
	"regexp"
	"strings"
)

var (
	// Structured JSON fields embedded in OWA boot data.
	reSmtp = regexp.MustCompile(`"(?:SmtpAddress|EmailAddress|PrimarySmtpAddress|MailboxAddress|mail|userPrincipalName)"\s*:\s*"([A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,})"`)
	// Page title: "Inbox - user@hotmail.com - Outlook" and variants.
	reTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	// Microsoft consumer domains, used to prefer plausible addresses.
	reMSMail  = regexp.MustCompile(`([A-Za-z0-9._%+\-]+@(?:hotmail|outlook|live|msn)\.[A-Za-z]{2,})`)
	reAnyMail = regexp.MustCompile(`([A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,})`)
	// Display name fields.
	reDisplay = regexp.MustCompile(`"(?:DisplayName|displayName|GivenName|FirstName|name)"\s*:\s*"([^"\\]{1,80})"`)
)

// ExtractIdentity pulls the account address and display name out of an
// authenticated OWA page. Returns empty strings when nothing matches —
// callers treat that as a live-but-unresolved session.
func ExtractIdentity(body string) (email, name string) {
	if m := reSmtp.FindStringSubmatch(body); len(m) > 1 {
		email = strings.TrimSpace(m[1])
	}

	if email == "" {
		if t := reTitle.FindStringSubmatch(body); len(t) > 1 {
			title := strings.TrimSpace(t[1])
			if m := reAnyMail.FindStringSubmatch(title); len(m) > 1 {
				email = strings.TrimSpace(m[1])
			}
			// "Inbox - Jane Doe - Outlook"
			parts := splitTitle(title)
			if len(parts) >= 3 && parts[1] != "" && !strings.Contains(parts[1], "@") {
				name = parts[1]
			}
		}
	}

	if email == "" {
		if m := reMSMail.FindStringSubmatch(body); len(m) > 1 {
			email = strings.TrimSpace(m[1])
		}
	}

	if email != "" && name == "" {
		if m := reDisplay.FindStringSubmatch(body); len(m) > 1 {
			name = strings.TrimSpace(m[1])
		}
	}

	return sanitise(email), sanitiseSpace(name)
}

func splitTitle(title string) []string {
	fields := strings.FieldsFunc(title, func(r rune) bool {
		return r == '-' || r == '–' || r == '—' || r == '|'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, strings.TrimSpace(f))
	}
	return out
}

func sanitise(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\x00' || r == '\n' || r == '\r' || r == '\t' || r == '|' {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

func sanitiseSpace(s string) string {
	s = sanitise(s)
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}
