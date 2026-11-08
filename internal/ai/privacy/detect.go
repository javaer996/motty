package privacy

import (
	"net"
	"regexp"
	"strconv"
	"strings"
)

var (
	// A value after password/token/secret/api_key, in the three shapes configs
	// and command lines use: double quoted, single quoted and bare.
	reAssignDouble = regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|token|api[_-]?key|apikey|access[_-]?key|private[_-]?key)\s*[:=]\s*"([^"\n]*)"`)
	reAssignSingle = regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|token|api[_-]?key|apikey|access[_-]?key|private[_-]?key)\s*[:=]\s*'([^'\n]*)'`)
	reAssignPlain  = regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|token|api[_-]?key|apikey|access[_-]?key|private[_-]?key)\s*[:=]\s*([^\s"',;&]+)`)

	reFlagDouble = regexp.MustCompile(`(?i)--(?:password|passwd|token|secret|api-key)[= ]\s*"([^"\n]*)"`)
	reFlagSingle = regexp.MustCompile(`(?i)--(?:password|passwd|token|secret|api-key)[= ]\s*'([^'\n]*)'`)
	reFlagPlain  = regexp.MustCompile(`(?i)--(?:password|passwd|token|secret|api-key)[= ]\s*([^\s"',;&]+)`)

	reBearer = regexp.MustCompile(`(?i)\bBearer\s+([A-Za-z0-9._~+/=-]{8,})`)
	reBasic  = regexp.MustCompile(`(?i)\bAuthorization\s*:\s*Basic\s+([A-Za-z0-9+/=]{8,})`)

	reAWS         = regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)
	reJWT         = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]*`)
	reVendorToken = regexp.MustCompile(`\b(?:ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{30,}|sk_(?:live|test|proj)_[A-Za-z0-9]{16,}|pk_(?:live|test)_[A-Za-z0-9]{16,}|glpat-[A-Za-z0-9_-]{16,})`)

	// scheme://user:password@host, or a lone userinfo used as a token.
	reURLUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^\s/@:]+)(?::([^\s/@]*))?@`)

	reSSHPublicKey = regexp.MustCompile(`(?i)\b(?:ssh-(?:rsa|dss|ed25519)|ecdsa-sha2-[a-z0-9-]+)\s+([A-Za-z0-9+/]{40,}={0,3})`)

	reHomePath = regexp.MustCompile(`(?i)(?:/home/|/Users/|\\Users\\)([A-Za-z0-9._-]+)`)

	// A prompt or ssh target. The host is left visible unless the hostname
	// rule is on; the user name is the identifying part here. The left side may
	// already be a token, because exact known values are replaced first.
	reUserAtHost = regexp.MustCompile(`(⟦[A-Z]+_[0-9]+⟧|\b[A-Za-z0-9._-]{1,64})@([A-Za-z0-9][A-Za-z0-9.-]{0,252})`)

	reSudoUser    = regexp.MustCompile(`\b(?:sudo|doas)\s+-u\s+([A-Za-z0-9._-]+)`)
	reRunuserUser = regexp.MustCompile(`\brunuser\s+(?:-u\s+)?([A-Za-z0-9._-]+)`)
	reSuUser      = regexp.MustCompile(`\bsu\s+(?:-\s+)?([A-Za-z][A-Za-z0-9._-]*)`)

	reEmail  = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)
	reDomain = regexp.MustCompile(`(?i)\b(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:com|net|org|io|dev|app|cloud|co|cn|uk|de|fr|jp|ru|local|lan|internal|corp|xyz|me|info|biz|tv|cc|edu|gov|mil|site|online|store|tech|pro)\b`)

	reFlatPEM = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*(?:PRIVATE KEY|CERTIFICATE)-----.*?-----END [A-Z0-9 ]*(?:PRIVATE KEY|CERTIFICATE)-----`)
)

// DefaultExcludedUsers are service accounts whose names identify nobody. The
// account filter leaves them readable so a prompt or a home path stays useful
// to the model.
var DefaultExcludedUsers = map[string]bool{
	"root": true, "www-data": true, "apache": true, "nginx": true, "postgres": true,
	"mysql": true, "mariadb": true, "redis": true, "mongodb": true, "docker": true,
	"git": true, "nobody": true, "daemon": true, "bin": true, "sys": true, "sync": true,
	"games": true, "man": true, "lp": true, "mail": true, "news": true, "uucp": true,
	"proxy": true, "backup": true, "list": true, "irc": true, "_apt": true, "ubuntu": true,
	"ec2-user": true, "admin": true, "administrator": true, "user": true, "test": true,
	"guest": true, "systemd-network": true, "systemd-resolve": true, "messagebus": true,
	"polkitd": true, "sshd": true, "chrony": true, "ntp": true, "syslog": true,
	"dbus": true, "tss": true, "sssd": true, "saslauth": true, "rtkit": true,
	"colord": true, "gdm": true, "avahi": true, "debian-exim": true, "statd": true,
	"rpc": true, "rpcuser": true, "nfsnobody": true, "vboxadd": true, "xrdp": true,
	"sddm": true, "lightdm": true, "netdata": true, "prometheus": true, "grafana": true,
	"jenkins": true, "runner": true, "gitlab-runner": true, "ansible": true,
}

// protectPEM replaces private key and certificate blocks. A block that never
// ends still hides everything after its BEGIN line: a truncated key is still
// a key.
func protectPEM(text string, replacements *[]Replacement, guard *Guard) string {
	text = replaceRegexWhole(text, reFlatPEM, func(match string) Category {
		if strings.Contains(match, "CERTIFICATE") {
			return CategoryCert
		}
		return CategoryKey
	}, guard, replacements)
	if !strings.Contains(text, "-----BEGIN ") {
		return text
	}
	var out strings.Builder
	for {
		start := strings.Index(text, "-----BEGIN ")
		if start < 0 {
			out.WriteString(text)
			return out.String()
		}
		line := text[start:]
		if newline := strings.IndexByte(line, '\n'); newline >= 0 {
			line = line[:newline]
		}
		category := CategoryKey
		switch {
		case strings.Contains(line, "CERTIFICATE"):
			category = CategoryCert
		case !strings.Contains(line, "PRIVATE KEY"):
			out.WriteString(text[:start+len("-----BEGIN ")])
			text = text[start+len("-----BEGIN "):]
			continue
		}
		out.WriteString(text[:start])
		rest := text[start:]
		end := strings.Index(rest, "-----END ")
		blockEnd := len(rest)
		trailingNewline := ""
		if end >= 0 {
			tail := rest[end:]
			if newline := strings.IndexByte(tail, '\n'); newline >= 0 {
				blockEnd = end + newline
				trailingNewline = "\n"
			}
		}
		if overlapsToken(tokenSpans(rest), 0, blockEnd) {
			// The block already contains a token (it was protected before this
			// call). Replacing it whole would hide the token inside another one.
			out.WriteString(rest[:len("-----BEGIN ")])
			text = rest[len("-----BEGIN "):]
			continue
		}
		block := rest[:blockEnd]
		replacement := guard.tokenFor(category, block)
		*replacements = appendReplacement(*replacements, replacement)
		out.WriteString(replacement.Token)
		out.WriteString(trailingNewline)
		text = rest[blockEnd+len(trailingNewline):]
	}
}

func protectSecrets(text string, replacements *[]Replacement, guard *Guard) string {
	text = protectURLUserinfo(text, replacements, guard)
	for _, rule := range []struct {
		re  *regexp.Regexp
		cat Category
	}{
		{reAssignDouble, CategorySecret},
		{reAssignSingle, CategorySecret},
		{reAssignPlain, CategorySecret},
		{reFlagDouble, CategorySecret},
		{reFlagSingle, CategorySecret},
		{reFlagPlain, CategorySecret},
		{reBearer, CategorySecret},
		{reBasic, CategorySecret},
	} {
		text = replaceRegexGroup(text, rule.re, 1, rule.cat, guard, replacements)
	}
	for _, rule := range []struct {
		re  *regexp.Regexp
		cat Category
	}{
		{reAWS, CategorySecret},
		{reJWT, CategorySecret},
		{reVendorToken, CategorySecret},
	} {
		text = replaceRegexWhole(text, rule.re, func(string) Category { return rule.cat }, guard, replacements)
	}
	return text
}

func protectURLUserinfo(text string, replacements *[]Replacement, guard *Guard) string {
	matches := reURLUserinfo.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	spans := tokenSpans(text)
	var out strings.Builder
	out.Grow(len(text))
	last := 0
	for _, match := range matches {
		if overlapsToken(spans, match[0], match[1]) {
			continue
		}
		schemeStart, schemeEnd := match[2], match[3]
		userStart, userEnd := match[4], match[5]
		passwordStart, passwordEnd := match[6], match[7]
		out.WriteString(text[last:schemeStart])
		out.WriteString(text[schemeStart:schemeEnd])
		user := text[userStart:userEnd]
		category := CategoryUser
		if passwordStart < 0 {
			// https://<token>@host: a lone userinfo is a credential.
			category = CategorySecret
		}
		userToken := guard.tokenFor(category, user)
		*replacements = appendReplacement(*replacements, userToken)
		out.WriteString(userToken.Token)
		if passwordStart >= 0 {
			out.WriteByte(':')
			if password := text[passwordStart:passwordEnd]; password != "" {
				passwordToken := guard.tokenFor(CategorySecret, password)
				*replacements = appendReplacement(*replacements, passwordToken)
				out.WriteString(passwordToken.Token)
			}
		}
		out.WriteByte('@')
		last = match[1]
	}
	out.WriteString(text[last:])
	return out.String()
}

func replaceRegexGroup(text string, re *regexp.Regexp, group int, category Category, guard *Guard, replacements *[]Replacement) string {
	matches := re.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	spans := tokenSpans(text)
	var out strings.Builder
	out.Grow(len(text))
	last := 0
	for _, match := range matches {
		start, end := match[2*group], match[2*group+1]
		if start < 0 || start == end || overlapsToken(spans, start, end) {
			continue
		}
		replacement := guard.tokenFor(category, text[start:end])
		*replacements = appendReplacement(*replacements, replacement)
		out.WriteString(text[last:start])
		out.WriteString(replacement.Token)
		last = end
	}
	out.WriteString(text[last:])
	return out.String()
}

func replaceRegexWhole(text string, re *regexp.Regexp, category func(string) Category, guard *Guard, replacements *[]Replacement) string {
	matches := re.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	spans := tokenSpans(text)
	var out strings.Builder
	out.Grow(len(text))
	last := 0
	for _, match := range matches {
		if overlapsToken(spans, match[0], match[1]) {
			continue
		}
		value := text[match[0]:match[1]]
		replacement := guard.tokenFor(category(value), value)
		*replacements = appendReplacement(*replacements, replacement)
		out.WriteString(text[last:match[0]])
		out.WriteString(replacement.Token)
		last = match[1]
	}
	out.WriteString(text[last:])
	return out.String()
}

func protectSSHKeys(text string, replacements *[]Replacement, guard *Guard) string {
	return replaceRegexGroup(text, reSSHPublicKey, 1, CategoryKey, guard, replacements)
}

func protectEmails(text string, replacements *[]Replacement, guard *Guard) string {
	return replaceRegexWhole(text, reEmail, func(string) Category { return CategoryEmail }, guard, replacements)
}

func protectDomains(text string, replacements *[]Replacement, guard *Guard) string {
	return replaceRegexWhole(text, reDomain, func(string) Category { return CategoryHost }, guard, replacements)
}

func protectIPs(text string, replacements *[]Replacement, guard *Guard) string {
	text = protectIPv4(text, replacements, guard)
	return protectIPv6(text, replacements, guard)
}

func protectIPv4(text string, replacements *[]Replacement, guard *Guard) string {
	spans := tokenSpans(text)
	var out strings.Builder
	changed := false
	last := 0
	for index := 0; index < len(text); {
		if !isDigit(text[index]) {
			index++
			continue
		}
		start := index
		for index < len(text) && (isDigit(text[index]) || text[index] == '.') {
			index++
		}
		candidate := text[start:index]
		value := strings.TrimRight(candidate, ".")
		tail := candidate[len(value):]
		if !validIPv4(value) || excludedIPv4(value) || overlapsToken(spans, start, index) {
			continue
		}
		if start > 0 && nameByte(text[start-1]) {
			continue
		}
		if index < len(text) && nameByte(text[index]) {
			continue
		}
		replacement := guard.tokenFor(CategoryIP, value)
		*replacements = appendReplacement(*replacements, replacement)
		if !changed {
			changed = true
			out.Grow(len(text))
		}
		out.WriteString(text[last:start])
		out.WriteString(replacement.Token)
		out.WriteString(tail)
		last = index
	}
	if !changed {
		return text
	}
	out.WriteString(text[last:])
	return out.String()
}

func validIPv4(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 3 {
			return false
		}
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 || number > 255 {
			return false
		}
	}
	return true
}

func excludedIPv4(value string) bool {
	return value == "0.0.0.0" || value == "255.255.255.255" || strings.HasPrefix(value, "127.")
}

func protectIPv6(text string, replacements *[]Replacement, guard *Guard) string {
	spans := tokenSpans(text)
	var out strings.Builder
	changed := false
	last := 0
	for index := 0; index < len(text); {
		if !isHexOrColon(text[index]) {
			index++
			continue
		}
		start := index
		for index < len(text) && isHexOrColon(text[index]) {
			index++
		}
		// A zone identifier belongs to the address for our purposes.
		if index < len(text) && text[index] == '%' {
			index++
			for index < len(text) && (isLetterOrDigit(text[index]) || text[index] == '_' || text[index] == '-' || text[index] == '.') {
				index++
			}
		}
		if overlapsToken(spans, start, index) {
			continue
		}
		candidate := text[start:index]
		value := strings.Trim(candidate, ".:")
		if strings.Count(value, ":") < 2 {
			continue
		}
		host := value
		if zone := strings.IndexByte(host, '%'); zone >= 0 {
			host = host[:zone]
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.To4() != nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		if start > 0 && nameByte(text[start-1]) {
			continue
		}
		if index < len(text) && nameByte(text[index]) {
			continue
		}
		replacement := guard.tokenFor(CategoryIP, value)
		*replacements = appendReplacement(*replacements, replacement)
		if !changed {
			changed = true
			out.Grow(len(text))
		}
		out.WriteString(text[last:start])
		out.WriteString(replacement.Token)
		last = index
	}
	if !changed {
		return text
	}
	out.WriteString(text[last:])
	return out.String()
}

func protectAccounts(text string, replacements *[]Replacement, guard *Guard) string {
	text = protectRegexGroupExcluded(text, reHomePath, 1, CategoryUser, guard, replacements)
	text = protectUserAtHost(text, replacements, guard)
	for _, re := range []*regexp.Regexp{reSudoUser, reRunuserUser, reSuUser} {
		text = protectRegexGroupExcluded(text, re, 1, CategoryUser, guard, replacements)
	}
	return text
}

func protectRegexGroupExcluded(text string, re *regexp.Regexp, group int, category Category, guard *Guard, replacements *[]Replacement) string {
	matches := re.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var out strings.Builder
	out.Grow(len(text))
	last := 0
	for _, match := range matches {
		start, end := match[2*group], match[2*group+1]
		if start < 0 || start == end {
			continue
		}
		value := text[start:end]
		if guard.cfg.ExcludedUsers[strings.ToLower(value)] {
			continue
		}
		replacement := guard.tokenFor(category, value)
		*replacements = appendReplacement(*replacements, replacement)
		out.WriteString(text[last:start])
		out.WriteString(replacement.Token)
		last = end
	}
	out.WriteString(text[last:])
	return out.String()
}

func protectUserAtHost(text string, replacements *[]Replacement, guard *Guard) string {
	matches := reUserAtHost.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var out strings.Builder
	out.Grow(len(text))
	last := 0
	for _, match := range matches {
		userStart, userEnd := match[2], match[3]
		hostStart, hostEnd := match[4], match[5]
		user := text[userStart:userEnd]
		host := text[hostStart:hostEnd]
		out.WriteString(text[last:userStart])
		switch {
		case strings.HasPrefix(user, TokenOpen):
			out.WriteString(user)
		case guard.cfg.ExcludedUsers[strings.ToLower(user)]:
			out.WriteString(user)
		default:
			replacement := guard.tokenFor(CategoryUser, user)
			*replacements = appendReplacement(*replacements, replacement)
			out.WriteString(replacement.Token)
		}
		out.WriteByte('@')
		hostValue := strings.TrimRight(host, ".")
		if guard.cfg.Hosts && hostValue != "" {
			replacement := guard.tokenFor(CategoryHost, hostValue)
			*replacements = appendReplacement(*replacements, replacement)
			out.WriteString(replacement.Token)
		} else {
			out.WriteString(hostValue)
		}
		out.WriteString(host[len(hostValue):])
		last = hostEnd
	}
	out.WriteString(text[last:])
	return out.String()
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func isLetterOrDigit(b byte) bool {
	return isDigit(b) || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isHexOrColon(b byte) bool {
	switch {
	case b >= '0' && b <= '9', b >= 'a' && b <= 'f', b >= 'A' && b <= 'F':
		return true
	case b == ':', b == '.':
		return true
	default:
		return false
	}
}
