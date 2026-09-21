// Package origin compiles and evaluates an allowlist of browser origins.
//
// It is its own package so that the HTTP layer which applies the policy and the
// config layer which validates it share one definition of what an origin is:
// internal/config cannot import internal/httpapi, and a second, subtly
// different parser living in config is how an allowlist ends up accepting at
// startup what it rejects at request time.
//
// # What an origin is
//
// An origin is a scheme, a host and an optional port — `https://app.swee.net`,
// `http://localhost:3000`. It carries no path, no query, no fragment and no
// credentials, and a browser serialises it in lower case with the scheme's
// default port omitted. Everything here is strict about that shape, because the
// matched value is echoed into a response header and the input is a request
// header an attacker controls on any non-browser client.
//
// # What it is not
//
// A CORS allowlist is not access control. It tells a browser which origins may
// read a response with JavaScript; it constrains nothing else, and anything
// that is not a browser ignores it entirely. The control that protects this API
// is the Bearer token. The allowlist narrows which pages may spend a token the
// browser already holds — worth having, but not a substitute for auth.
package origin

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// Wildcard is the pattern that admits every origin. It is the policy both
// sister services (countinghouse and greenhouse) apply unconditionally, offered
// here as one explicit allowlist entry so that an operator who wants it has a
// supported way to ask rather than reaching for "https://*".
const Wildcard = "*"

// Policy is a compiled allowlist.
//
// The zero value admits nothing, so a caller that forgets to Compile fails
// closed. That is deliberate: every other bug in this package produces a denial
// somebody notices, and only this one would produce a silent allow.
type Policy struct {
	allowAll bool
	rules    []rule
}

// rule is one compiled allowlist entry.
//
// host and suffix are mutually exclusive: an entry either names a host exactly
// or names a parent domain whose subdomains it admits.
type rule struct {
	scheme  string // "http" or "https", lower case
	host    string // exact host, when suffix is empty
	suffix  string // ".swee.net", admitting any subdomain of swee.net
	port    string // "" requires the origin to carry no port
	anyPort bool   // set by a ":*" suffix; ignores port entirely
}

var (
	errNoScheme = errors.New(`no scheme: an origin is written "https://host" or ` +
		`"http://host:port", never as a bare host`)
	errScheme      = errors.New("unsupported scheme: a browser origin is http or https")
	errNotAnOrigin = errors.New("an origin carries a scheme, a host and an optional port only — " +
		"no path, query, fragment or credentials")
	errEmptyEntry  = errors.New("empty entry")
	errNoHost      = errors.New("no host")
	errHostTooLong = errors.New("host is longer than DNS permits (253 characters)")
	errBadLabel    = errors.New("host is not a hostname or IP literal: labels are 1-63 characters " +
		"of a-z, 0-9 and '-', and may not begin or end with '-'")
	errBadPort  = errors.New("port is not a number in 1-65535")
	errWildcard = errors.New(`a wildcard replaces the whole leading label and nothing else: ` +
		`write "https://*.example.com", not "https://ap*.example.com" or "https://a.*.example.com"`)
)

// Compile turns allowlist patterns into a Policy.
//
// Accepted forms:
//
//   - every origin (a flat wildcard, not a per-origin echo)
//     https://app.swee.net    one origin exactly
//     https://*.swee.net      any subdomain of swee.net, but not swee.net itself
//     http://localhost:*      any port on that host, including none
//     https://*.swee.net:*    the two wildcards combine
//
// A malformed pattern is an error rather than an entry that is quietly skipped.
// A skipped entry fails silently in both directions — the operator believes an
// origin is allowlisted when it is not, or believes one is excluded when the
// rule meant to exclude it never parsed — and neither is visible from the
// server side. Refusing is the same trade this service already makes for an
// unset site and an unnamed devices namespace.
//
// On any error the returned Policy is the zero value, so a caller that ignores
// the error still denies everything rather than applying a half-built list.
func Compile(patterns []string) (Policy, error) {
	var p Policy
	for _, raw := range patterns {
		// Trimmed because a stray space from a hand-edited YAML allowlist is
		// the likeliest mistake here, and it lands on an error path that
		// refuses the start — reporting it as a bad scheme or bad hostname
		// syntax would send the operator looking in the wrong place. Only the
		// surrounding whitespace goes: "https://app swee.net" is a malformed
		// origin, not a typo with an obvious intent.
		entry := strings.TrimSpace(raw)
		if entry == "" {
			return Policy{}, fmt.Errorf("%q is not a usable origin pattern: %w", raw, errEmptyEntry)
		}
		if entry == Wildcard {
			p.allowAll = true
			continue
		}
		r, err := compileRule(entry)
		if err != nil {
			return Policy{}, fmt.Errorf("%q is not a usable origin pattern: %w", raw, err)
		}
		p.rules = append(p.rules, r)
	}
	return p, nil
}

// Enabled reports whether any origin could be allowed. An empty policy is the
// pre-CORS behaviour of the service and a perfectly valid configuration.
func (p Policy) Enabled() bool { return p.allowAll || len(p.rules) > 0 }

// Allow reports whether o may read a cross-origin response, and returns the
// value to send as Access-Control-Allow-Origin.
//
// The returned value is a normalised serialisation of the origin rather than
// the caller's bytes. A browser compares the header byte-for-byte against its
// own serialisation, which is lower case, so echoing back an unusual casing
// would produce a header the browser rejects — and normalising also means
// casing can never smuggle a distinct value into a response header.
//
// Under the allow-all policy the value is a flat "*": one answer for every
// origin, cacheable once, and accepted by a browser even for the opaque "null"
// origin that a sandboxed iframe or a file:// page presents. That is only sound
// because this API authenticates by Bearer token and never sends
// Access-Control-Allow-Credentials, with which "*" is invalid.
//
// An empty o means the request carried no Origin header — it is not
// cross-origin, and gets nothing.
func (p Policy) Allow(o string) (string, bool) {
	if o == "" {
		return "", false
	}
	if p.allowAll {
		return Wildcard, true
	}
	if len(p.rules) == 0 {
		return "", false
	}
	scheme, host, port, err := split(o)
	if err != nil {
		return "", false
	}
	// "null" and every other malformed value falls out here. An opaque origin
	// is the *absence* of a host, so no allowlist entry can name it: a
	// sandboxed iframe, a file:// page and a cross-site redirect all present
	// it, and treating it as allowlistable would hand them the API.
	canonical, err := validateHost(host)
	if err != nil {
		return "", false
	}
	if port != "" && validatePort(port) != nil {
		return "", false
	}
	for _, r := range p.rules {
		if r.match(scheme, canonical, port) {
			return serialise(scheme, canonical, port), true
		}
	}
	return "", false
}

func (r rule) match(scheme, host, port string) bool {
	if r.scheme != scheme {
		return false
	}
	if !r.anyPort && r.port != port {
		return false
	}
	if r.suffix != "" {
		// len >, not >=: the suffix is ".swee.net", so a match needs at least
		// one character of subdomain in front of it. This is also what keeps
		// "evil-swee.net" out — it ends with "-swee.net", not ".swee.net" —
		// which is the bug a bare strings.HasSuffix("swee.net") would have.
		return len(host) > len(r.suffix) && strings.HasSuffix(host, r.suffix)
	}
	return r.host == host
}

func compileRule(raw string) (rule, error) {
	scheme, host, port, err := split(raw)
	if err != nil {
		return rule{}, err
	}
	r := rule{scheme: scheme}
	if port == Wildcard {
		r.anyPort = true
	} else {
		if port != "" {
			if err := validatePort(port); err != nil {
				return rule{}, err
			}
		}
		r.port = port
	}

	if host == Wildcard {
		return rule{}, fmt.Errorf(`"%s://*" admits every origin; write %q if that is what you mean`,
			scheme, Wildcard)
	}
	if parent, isWild := strings.CutPrefix(host, "*."); isWild {
		if strings.Contains(parent, Wildcard) {
			return rule{}, errWildcard
		}
		// An address has no subdomains, so a wildcard over one is a rule that
		// could never fire. Refusing beats accepting a silent no-op.
		//
		// The ParseIP arm catches the likelier spelling: digits and dots are
		// valid label characters, so "*.192.168.1.1" would otherwise pass as a
		// hostname with two labels and match only "a.192.168.1.1" — never the
		// address someone allowlisting a LAN host meant.
		if strings.HasPrefix(parent, "[") || net.ParseIP(parent) != nil {
			return rule{}, errors.New("a wildcard has no meaning over an IP literal: " +
				"an address has no subdomains")
		}
		canonical, err := validateHost(parent)
		if err != nil {
			return rule{}, err
		}
		// A wildcard needs a parent of at least two labels. That rules out the
		// bare TLDs and "*.localhost", whose single label would otherwise admit
		// every name a resolver invents; the known single-label host is named
		// exactly, as "http://localhost:*".
		//
		// This is NOT a public-suffix check and does not pretend to be one: a
		// two-label parent can still be a public suffix, so "*.co.uk" and
		// "*.github.io" both compile. Doing that properly would mean depending
		// on golang.org/x/net/publicsuffix, which is more machinery than a
		// hand-edited allowlist warrants — the operator reading the entry is
		// the check.
		if strings.Count(canonical, ".") < 1 {
			return rule{}, fmt.Errorf("%q is too broad to allowlist: a wildcard needs a parent "+
				`domain of at least two labels, and a single-label host is named exactly `+
				`(e.g. "http://localhost:*")`, host)
		}
		r.suffix = "." + canonical
		return r, nil
	}
	if strings.Contains(host, Wildcard) {
		return rule{}, errWildcard
	}
	canonical, err := validateHost(host)
	if err != nil {
		return rule{}, err
	}
	r.host = canonical
	return r, nil
}

// split breaks an origin or a pattern into its three parts, lower-casing the
// scheme and host. It validates the overall shape but not the host or port
// contents, so that compileRule can allow the wildcards there that an actual
// origin may not carry.
func split(raw string) (scheme, host, port string, err error) {
	i := strings.Index(raw, "://")
	if i < 0 {
		return "", "", "", errNoScheme
	}
	scheme = strings.ToLower(raw[:i])
	if scheme != "http" && scheme != "https" {
		return "", "", "", errScheme
	}
	rest := raw[i+len("://"):]
	if rest == "" {
		return "", "", "", errNoHost
	}
	// Reject everything that would make this a URL rather than an origin before
	// looking at the host, so that "https://evil.com@app.swee.net" and
	// "https://app.swee.net/../x" are refused outright rather than parsed into
	// something that happens to match.
	if strings.ContainsAny(rest, "/?#@\\") {
		return "", "", "", errNotAnOrigin
	}

	// Lift a leading wildcard label off before anything else, and put it back
	// on the host at the end. Without this a bracketed literal behind a "*."
	// never reaches the IPv6 path below — the bracket is no longer the first
	// character — so "https://*.[::1]" falls through to the port split and the
	// colons inside the address get read as a port separator, reporting a bad
	// port for what is really "an address has no subdomains".
	wild := ""
	if parent, isWild := strings.CutPrefix(rest, "*."); isWild {
		wild, rest = "*.", parent
	}

	if strings.HasPrefix(rest, "[") {
		// IPv6 literal: the brackets are part of the host, and the only colon
		// that separates a port is the one after "]".
		end := strings.Index(rest, "]")
		if end < 0 {
			return "", "", "", errBadLabel
		}
		host, rest = rest[:end+1], rest[end+1:]
		switch {
		case rest == "":
		case strings.HasPrefix(rest, ":"):
			port = rest[1:]
			if port == "" {
				return "", "", "", errBadPort
			}
		default:
			return "", "", "", errBadLabel
		}
		return scheme, wild + strings.ToLower(host), port, nil
	}

	if c := strings.LastIndex(rest, ":"); c >= 0 {
		host, port = rest[:c], rest[c+1:]
		if port == "" {
			return "", "", "", errBadPort
		}
	} else {
		host = rest
	}
	return scheme, wild + strings.ToLower(host), port, nil
}

// validateHost accepts a hostname, an IPv4 address or a bracketed IPv6 literal,
// and nothing else, returning the host in canonical form. It is the gate that
// keeps control characters, spaces and header-splitting shapes out of a value
// destined for a response header.
//
// Canonicalising matters for IPv6 in the same way lower-casing matters for a
// hostname. One address has many spellings and a browser only ever sends the
// compressed RFC 5952 form, so matching them as text would mean an operator who
// writes "[0:0:0:0:0:0:0:1]" gets an entry that compiles, validates, starts the
// service and never matches anything. Both sides go through here, so the
// spelling cannot matter on either.
func validateHost(h string) (string, error) {
	if h == "" {
		return "", errNoHost
	}
	if strings.HasPrefix(h, "[") {
		// The unbalanced case cannot arrive from split, which only produces a
		// bracketed host once it has found the closing bracket — but the two
		// are checked together so this stays correct if that ever changes.
		inner, balanced := strings.CutSuffix(strings.TrimPrefix(h, "["), "]")
		ip := net.ParseIP(inner)
		if !balanced || ip == nil {
			return "", errBadLabel
		}
		// Brackets mean IPv6. An IPv4 address inside them — written directly or
		// as the IPv4-mapped "::ffff:127.0.0.1" — canonicalises through
		// net.IP.String() to a dotted quad, and "[127.0.0.1]" is not a host any
		// browser sends or any parser should emit. Refuse rather than echo it.
		if ip.To4() != nil {
			return "", errors.New("an IPv4 address is written without brackets " +
				`(e.g. "http://127.0.0.1:3000"); brackets are for IPv6`)
		}
		return "[" + ip.String() + "]", nil
	}
	if len(h) > 253 {
		return "", errHostTooLong
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 {
			return "", errBadLabel
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", errBadLabel
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return "", errBadLabel
			}
		}
	}
	return h, nil
}

// validatePort accepts the decimal ports a browser serialises: 1-65535, no
// leading zero. Port 0 and "0080" are not forms an Origin header ever carries.
func validatePort(p string) error {
	if len(p) == 0 || len(p) > 5 || p[0] == '0' {
		return errBadPort
	}
	n := 0
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c < '0' || c > '9' {
			return errBadPort
		}
		n = n*10 + int(c-'0')
	}
	if n > 65535 {
		return errBadPort
	}
	return nil
}

func serialise(scheme, host, port string) string {
	if port == "" {
		return scheme + "://" + host
	}
	return scheme + "://" + host + ":" + port
}
