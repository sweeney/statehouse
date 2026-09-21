package origin

import "testing"

// compile is a test helper that fails the test when a pattern set that is
// expected to be valid does not compile.
func compile(t *testing.T, patterns ...string) Policy {
	t.Helper()
	p, err := Compile(patterns)
	if err != nil {
		t.Fatalf("Compile(%v) returned error: %v", patterns, err)
	}
	return p
}

// allowed asserts that an origin is allowed and that the value echoed back is
// the one given. The echoed value matters as much as the boolean: a browser
// compares Access-Control-Allow-Origin byte-for-byte against its own
// serialisation of the origin, so a value that is merely "equivalent" fails.
func allowed(t *testing.T, p Policy, o, want string) {
	t.Helper()
	got, ok := p.Allow(o)
	if !ok {
		t.Fatalf("Allow(%q) = not allowed, want allowed", o)
	}
	if got != want {
		t.Errorf("Allow(%q) echoed %q, want %q", o, got, want)
	}
}

func denied(t *testing.T, p Policy, o string) {
	t.Helper()
	if got, ok := p.Allow(o); ok {
		t.Errorf("Allow(%q) = %q, allowed; want denied", o, got)
	}
}

// ── The empty policy ─────────────────────────────────────────────────────────

// No configured origins means no origin is allowed. This is the pre-CORS
// behaviour of the service and must stay reachable, so that deploying this
// change without a config edit changes nothing for the allowlisted routes.
func TestEmptyPolicyAllowsNothing(t *testing.T) {
	p := compile(t)
	if p.Enabled() {
		t.Error("Enabled() = true for an empty policy, want false")
	}
	for _, o := range []string{"https://app.swee.net", "http://localhost:3000", "null", ""} {
		denied(t, p, o)
	}
}

// A nil slice is the zero value a Server carries when nothing sets the field,
// and must behave exactly like an empty one rather than panicking.
func TestNilPatternsCompile(t *testing.T) {
	p, err := Compile(nil)
	if err != nil {
		t.Fatalf("Compile(nil) returned error: %v", err)
	}
	if p.Enabled() {
		t.Error("Enabled() = true for a nil pattern set, want false")
	}
	denied(t, p, "https://app.swee.net")
}

// The zero Policy is what a caller gets if it forgets to Compile. It must deny
// rather than allow: a matcher that fails open is the one bug in this file that
// would not be noticed until it mattered.
func TestZeroPolicyDenies(t *testing.T) {
	var p Policy
	if p.Enabled() {
		t.Error("Enabled() = true for the zero Policy, want false")
	}
	denied(t, p, "https://app.swee.net")
}

// ── Exact origins ────────────────────────────────────────────────────────────

func TestExactOriginMatches(t *testing.T) {
	p := compile(t, "https://app.swee.net")
	allowed(t, p, "https://app.swee.net", "https://app.swee.net")
	denied(t, p, "https://other.swee.net")
	denied(t, p, "https://app.swee.net.evil.com")
}

// Scheme is part of the origin. An allowlisted https origin does not admit its
// http twin: that would let a network attacker who can serve plaintext on the
// same host read the API.
func TestExactOriginIsSchemeSensitive(t *testing.T) {
	p := compile(t, "https://app.swee.net")
	denied(t, p, "http://app.swee.net")
}

// Port is part of the origin too. A pattern naming no port matches only an
// origin carrying none — which is how a browser serialises a default-port URL.
func TestExactOriginIsPortSensitive(t *testing.T) {
	p := compile(t, "http://localhost:3000")
	allowed(t, p, "http://localhost:3000", "http://localhost:3000")
	denied(t, p, "http://localhost:3001")
	denied(t, p, "http://localhost")

	noPort := compile(t, "https://app.swee.net")
	denied(t, noPort, "https://app.swee.net:8443")
	// A browser never serialises the scheme's default port into an Origin, so
	// this form only ever arrives from a non-browser client.
	denied(t, noPort, "https://app.swee.net:443")
}

// Several patterns compose, and first match wins without disturbing the rest.
func TestMultiplePatterns(t *testing.T) {
	p := compile(t, "https://app.swee.net", "http://localhost:3000", "https://dash.example.org")
	allowed(t, p, "http://localhost:3000", "http://localhost:3000")
	allowed(t, p, "https://dash.example.org", "https://dash.example.org")
	denied(t, p, "https://nope.example.org")
}

// ── Subdomain wildcards ──────────────────────────────────────────────────────

// The distinction the issue asks to pin: "*.swee.net" covers subdomains, and
// nothing else. `evil-swee.net` is the classic suffix-matching bug — a naive
// strings.HasSuffix("swee.net") hands the whole API to a domain an attacker can
// register. The apex is excluded too: a wildcard is not a licence for the
// parent, and listing it is one line.
func TestSubdomainWildcard(t *testing.T) {
	p := compile(t, "https://*.swee.net")
	allowed(t, p, "https://app.swee.net", "https://app.swee.net")
	allowed(t, p, "https://statehouse.swee.net", "https://statehouse.swee.net")

	denied(t, p, "https://evil-swee.net")
	denied(t, p, "https://sweeXnet")
	denied(t, p, "https://swee.net")
	denied(t, p, "https://swee.net.evil.com")
	denied(t, p, "https://app.swee.net.evil.com")
}

// A wildcard covers nested subdomains as well as immediate ones. Every label
// under swee.net is controlled by whoever controls swee.net, so the depth
// carries no extra trust; being explicit about it stops the question recurring.
func TestSubdomainWildcardMatchesNestedLabels(t *testing.T) {
	p := compile(t, "https://*.swee.net")
	allowed(t, p, "https://a.b.swee.net", "https://a.b.swee.net")
}

// The apex is admitted by listing it, alongside the wildcard.
func TestApexAdmittedWhenListed(t *testing.T) {
	p := compile(t, "https://*.swee.net", "https://swee.net")
	allowed(t, p, "https://swee.net", "https://swee.net")
	allowed(t, p, "https://app.swee.net", "https://app.swee.net")
}

// A wildcard is still scheme- and port-bound.
func TestSubdomainWildcardKeepsSchemeAndPort(t *testing.T) {
	p := compile(t, "https://*.swee.net")
	denied(t, p, "http://app.swee.net")
	denied(t, p, "https://app.swee.net:8443")
}

// ── Port wildcards ───────────────────────────────────────────────────────────

// The practical blocker in the issue: nobody can prototype a browser client
// without this, because a dev server's port is not knowable in advance.
func TestPortWildcard(t *testing.T) {
	p := compile(t, "http://localhost:*")
	allowed(t, p, "http://localhost:3000", "http://localhost:3000")
	allowed(t, p, "http://localhost:5173", "http://localhost:5173")
	allowed(t, p, "http://localhost:8080", "http://localhost:8080")
	// A port wildcard covers the portless form too: "any port, including none".
	allowed(t, p, "http://localhost", "http://localhost")

	denied(t, p, "https://localhost:3000")
	denied(t, p, "http://localhost.evil.com:3000")
	denied(t, p, "http://notlocalhost:3000")
}

// Host and port wildcards compose.
func TestHostAndPortWildcardCombined(t *testing.T) {
	p := compile(t, "https://*.swee.net:*")
	allowed(t, p, "https://app.swee.net:8443", "https://app.swee.net:8443")
	allowed(t, p, "https://app.swee.net", "https://app.swee.net")
	denied(t, p, "https://swee.net:8443")
}

// ── The allow-all pattern ────────────────────────────────────────────────────

// "*" is the sister services' policy (countinghouse and greenhouse both send a
// flat wildcard) spelled as one allowlist entry, so an operator who wants that
// behaviour has a supported way to ask for it.
//
// It emits the literal "*" rather than echoing the origin. That is what makes
// the response cacheable once for every origin, and it is only sound because
// the API authenticates by Bearer token and never sends
// Access-Control-Allow-Credentials.
func TestAllowAll(t *testing.T) {
	p := compile(t, "*")
	if !p.Enabled() {
		t.Error("Enabled() = false for the allow-all policy, want true")
	}
	allowed(t, p, "https://anything.example.com", "*")
	allowed(t, p, "http://localhost:3000", "*")
	// "null" is what a sandboxed iframe, a file:// page and a redirected
	// request all send. A flat wildcard covers it, because the browser accepts
	// "*" for an opaque origin.
	allowed(t, p, "null", "*")
	// An absent Origin is not a cross-origin request at all.
	denied(t, p, "")
}

// Allow-all wins regardless of where it appears, and does not tempt the matcher
// into echoing an origin it happened to also match.
func TestAllowAllWithOtherPatterns(t *testing.T) {
	p := compile(t, "https://app.swee.net", "*")
	allowed(t, p, "https://app.swee.net", "*")
	allowed(t, p, "https://elsewhere.example.com", "*")
}

// ── Opaque and malformed origins ─────────────────────────────────────────────

// "null" must never be matched by an allowlist entry. It is not a host, it is
// the absence of one — a sandboxed iframe, a file:// page or a redirect can all
// present it, so treating it as allowlistable would hand those the API.
func TestNullOriginNeverMatchesAnAllowlist(t *testing.T) {
	p := compile(t, "https://*.swee.net", "http://localhost:*", "https://app.swee.net")
	denied(t, p, "null")
	denied(t, p, "NULL")
}

// An empty Origin header means the request is not cross-origin. Nothing is
// echoed, under any policy.
func TestEmptyOriginDenied(t *testing.T) {
	denied(t, compile(t, "https://*.swee.net"), "")
}

// The value reaching Allow comes from a request header, so it is attacker
// controlled on any non-browser client. Nothing here may be echoed into a
// response header, and nothing may be coaxed into matching by a delimiter the
// parser treats differently from the way a browser would.
func TestMalformedOriginsDenied(t *testing.T) {
	p := compile(t, "https://*.swee.net", "http://localhost:*", "https://app.swee.net")
	for _, o := range []string{
		"https://app.swee.net/",             // origins carry no path
		"https://app.swee.net/../evil.com",  //
		"https://app.swee.net:3000/x",       //
		"https://evil.com@app.swee.net",     // userinfo is not part of an origin
		"https://app.swee.net#x",            //
		"https://app.swee.net?x=1",          //
		"https://app.swee.net\r\nX-Evil: 1", // response-splitting shape
		"https://app.swee.net\nX-Evil: 1",   //
		"https://app.swee.net ",             // trailing space
		" https://app.swee.net",             // leading space
		"https://app swee.net",              //
		"app.swee.net",                      // no scheme
		"//app.swee.net",                    // scheme-relative
		"ftp://app.swee.net",                // unsupported scheme
		"file://app.swee.net",               //
		"javascript:alert(1)",               //
		"data:text/html,x",                  //
		"https://",                          // no host
		"https://:3000",                     // no host, just a port
		"https://app.swee.net:",             // empty port
		"https://app.swee.net:abc",          // non-numeric port
		"https://app.swee.net:99999",        // out of range
		"https://app.swee.net:-1",           //
		"https://.swee.net",                 // empty leading label
		"https://app..swee.net",             // empty interior label
		"https://app.swee.net.",             // trailing dot
		"https://-app.swee.net",             // label may not start with a hyphen
		"https://app-.swee.net",             // or end with one
		"https://*.swee.net",                // a pattern is not an origin
		"*",                                 //
	} {
		denied(t, p, o)
	}
}

// A host longer than DNS permits is rejected rather than echoed. Bounding the
// echoed value is the point: it is copied into a response header.
func TestOverlongHostDenied(t *testing.T) {
	long := "https://"
	for i := 0; i < 20; i++ {
		long += "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa." // 20 * 30 = 600 chars
	}
	denied(t, compile(t, "https://*.example.com"), long+"example.com")
}

// A single label may not exceed 63 characters.
func TestOverlongLabelDenied(t *testing.T) {
	label := ""
	for i := 0; i < 64; i++ {
		label += "a"
	}
	denied(t, compile(t, "https://*.swee.net"), "https://"+label+".swee.net")
}

// Scheme and host are case-insensitive per RFC 3986, and a browser serialises
// both in lower case. The echoed value is normalised so that a client sending
// an unusual casing is still answered with something its browser will accept
// byte-for-byte — and so that casing can never be used to smuggle a distinct
// value into the response header.
func TestOriginCaseIsNormalised(t *testing.T) {
	p := compile(t, "https://app.swee.net")
	allowed(t, p, "HTTPS://APP.SWEE.NET", "https://app.swee.net")
	allowed(t, p, "https://App.Swee.Net", "https://app.swee.net")
}

// IP-literal origins are matched by address, not by spelling. A LAN deployment
// is a plausible place to want one.
func TestIPLiteralOrigins(t *testing.T) {
	p := compile(t, "http://192.168.1.10:8080", "http://[::1]:3000")
	allowed(t, p, "http://192.168.1.10:8080", "http://192.168.1.10:8080")
	allowed(t, p, "http://[::1]:3000", "http://[::1]:3000")
	denied(t, p, "http://192.168.1.11:8080")
	denied(t, p, "http://[::2]:3000")
}

// An IPv6 address has many spellings and a browser only ever sends one of them
// (the compressed RFC 5952 form). Matching them as text would mean an operator
// who writes the expanded literal gets an entry that compiles, validates,
// starts the service — and never matches anything: the silent-in-both-
// directions failure the refuse-to-start design exists to prevent, relocated
// from "malformed" to "differently spelled". So both sides canonicalise.
func TestIPv6SpellingsAreCanonicalised(t *testing.T) {
	// The expanded form in the config matches what a browser actually sends.
	expanded := compile(t, "http://[0:0:0:0:0:0:0:1]:3000")
	allowed(t, expanded, "http://[::1]:3000", "http://[::1]:3000")

	// And the reverse: a compressed entry matches an expanded origin, which is
	// what a non-browser client might send.
	compressed := compile(t, "http://[::1]:3000")
	allowed(t, compressed, "http://[0:0:0:0:0:0:0:1]:3000", "http://[::1]:3000")

	// Upper-case hex and the IPv4-mapped notations collapse too. The echoed
	// value is always the canonical form, for the same reason the doc comment
	// gives for casing: a browser compares it byte-for-byte.
	upper := compile(t, "http://[2001:DB8::1]")
	allowed(t, upper, "http://[2001:db8:0:0:0:0:0:1]", "http://[2001:db8::1]")

}

// An IPv4 address in brackets is refused rather than canonicalised. net.IP
// renders both the direct and the IPv4-mapped spelling as a dotted quad, and
// "[127.0.0.1]" is not a host a browser sends or a parser should emit — so
// echoing it would put a malformed value in a response header.
func TestBracketedIPv4Rejected(t *testing.T) {
	for _, pattern := range []string{
		"http://[127.0.0.1]:8080",
		"http://[::ffff:127.0.0.1]:8080",
		"http://[::ffff:7f00:1]:8080",
	} {
		if _, err := Compile([]string{pattern}); err == nil {
			t.Errorf("Compile(%q) = nil error, want a rejection", pattern)
		}
	}
	// And the same value arriving as an Origin header is never echoed.
	p := compile(t, "http://[::1]:8080")
	denied(t, p, "http://[::ffff:127.0.0.1]:8080")
}

// A wildcard over an IP address can never match anything — there are no
// subdomains of an address — so it is refused rather than accepted as a rule
// that silently never fires. "https://*.192.168.1.1" is the likely spelling:
// digits and dots are valid label characters, so without this it compiles,
// validates, starts the service, and matches only nonsense hostnames like
// "a.192.168.1.1" rather than the address the operator wrote it for.
//
// Every spelling reaches the same message, including the bracketed forms with
// no port — those parse through the port split first, so the check has to sit
// ahead of it rather than behind.
func TestCompileRejectsAWildcardOverAnIPLiteral(t *testing.T) {
	for _, pattern := range []string{
		"https://*.192.168.1.1",
		"https://*.192.168.1.1:*",
		"https://*.192.168.1.1:8080",
		"http://*.[::1]",
		"http://*.[::1]:3000",
		"https://*.[2001:db8::1]:*",
	} {
		_, err := Compile([]string{pattern})
		if err == nil {
			t.Errorf("Compile(%q) = nil error, want a rejection", pattern)
			continue
		}
		if !contains(err.Error(), "IP literal") {
			t.Errorf("Compile(%q) error = %q, want it to name the IP literal as the problem",
				pattern, err)
		}
	}
}

// A hostname that merely looks numeric is still a hostname.
func TestWildcardOverANumericHostnameIsStillAllowed(t *testing.T) {
	p := compile(t, "https://*.1.2.3.4.5")
	allowed(t, p, "https://a.1.2.3.4.5", "https://a.1.2.3.4.5")
}

// An IP address named exactly is unaffected — only the wildcard form is refused.
func TestExactIPAddressesStillCompile(t *testing.T) {
	p := compile(t, "https://192.168.1.1:8080", "http://[::1]:3000")
	allowed(t, p, "https://192.168.1.1:8080", "https://192.168.1.1:8080")
	allowed(t, p, "http://[::1]:3000", "http://[::1]:3000")
}

// ── Whitespace ───────────────────────────────────────────────────────────────

// A stray space from a hand-edited YAML allowlist is about the likeliest
// mistake in this list, and it lands on an error path that refuses the start.
// Reporting it as "unsupported scheme" or as hostname syntax sends the operator
// looking in the wrong place, so entries are trimmed.
func TestCompileTrimsSurroundingWhitespace(t *testing.T) {
	p := compile(t, "  https://app.swee.net", "http://localhost:*\t")
	allowed(t, p, "https://app.swee.net", "https://app.swee.net")
	allowed(t, p, "http://localhost:3000", "http://localhost:3000")

	// The allow-all entry is recognised after trimming too.
	all := compile(t, " * ")
	allowed(t, all, "https://anywhere.example.com", "*")
}

// An entry that is only whitespace is an empty entry, and says so rather than
// complaining about a missing scheme.
func TestCompileRejectsAnEmptyEntry(t *testing.T) {
	for _, pattern := range []string{"", " ", "\t\n"} {
		_, err := Compile([]string{pattern})
		if err == nil {
			t.Fatalf("Compile(%q) = nil error, want a rejection", pattern)
		}
		if !contains(err.Error(), "empty") {
			t.Errorf("Compile(%q) error = %q, want it to name the entry as empty", pattern, err)
		}
	}
}

// Whitespace *inside* an entry is still a malformed origin, not something to
// tidy away: "https://app swee.net" is not a typo with an obvious intent.
func TestCompileRejectsInteriorWhitespace(t *testing.T) {
	if _, err := Compile([]string{"https://app swee.net"}); err == nil {
		t.Error("Compile = nil error for an origin with an interior space, want a rejection")
	}
}

// An Origin *header* is not trimmed. A browser never sends a padded one, and
// accepting padding would mean two spellings of one origin reach the matcher.
func TestOriginHeaderIsNotTrimmed(t *testing.T) {
	p := compile(t, "https://app.swee.net")
	denied(t, p, " https://app.swee.net")
	denied(t, p, "https://app.swee.net ")
}

// ── Pattern validation ───────────────────────────────────────────────────────

// A bad pattern is a typo in a security control: the operator believes an
// origin is allowlisted and it silently is not, or — worse — believes one is
// excluded. Compile refuses rather than skipping the entry, so the service
// declines to start and says which entry is wrong.
func TestCompileRejectsBadPatterns(t *testing.T) {
	for _, pattern := range []string{
		"",                        // an empty entry is a YAML slip
		" ",                       //
		"app.swee.net",            // no scheme
		"//app.swee.net",          //
		"*.swee.net",              // scheme is not optional, even for a wildcard
		"ftp://app.swee.net",      // only http and https are browser origins
		"https://app.swee.net/",   // a trailing slash makes it a URL, not an origin
		"https://app.swee.net/ui", //
		"https://*",               // allow-everything, ambiguously spelled
		"https://*.*",             //
		"https://*:*",             //
		"https://ap*.swee.net",    // only a whole leading label may be wildcarded
		"https://*swee.net",       //
		"https://*.swee.*",        // the wildcard belongs at the front
		"https://app.*.swee.net",  // and only at the front
		"https://app.swee.net:",   // empty port
		"https://app.swee.net:ab", //
		"https://",                // no host
		"https://app swee.net",    //
	} {
		if _, err := Compile([]string{pattern}); err == nil {
			t.Errorf("Compile(%q) = nil error, want a rejection", pattern)
		}
	}
}

// A wildcard needs a parent of at least two labels. That rules out the bare
// TLDs and "*.localhost", whose single label would otherwise admit every name a
// resolver invents.
//
// It is NOT a public-suffix check, and deliberately is not — see
// TestTwoLabelParentIsNotARegistrableDomainCheck.
func TestCompileRejectsASingleLabelWildcardParent(t *testing.T) {
	for _, pattern := range []string{"https://*.net", "http://*.localhost", "https://*.com"} {
		if _, err := Compile([]string{pattern}); err == nil {
			t.Errorf("Compile(%q) = nil error, want a rejection", pattern)
		}
	}
}

// The label-count rule is a guard against the most obvious footgun, not a
// registrable-domain check: a two-label parent can still be a public suffix,
// and "https://*.co.uk" compiles. Pinned as a test so that nobody reads the
// rejection above and concludes public suffixes are handled — they are not, and
// doing so properly would mean depending on golang.org/x/net/publicsuffix.
func TestTwoLabelParentIsNotARegistrableDomainCheck(t *testing.T) {
	p := compile(t, "https://*.co.uk", "https://*.github.io")
	allowed(t, p, "https://anyone.co.uk", "https://anyone.co.uk")
	allowed(t, p, "https://anyone.github.io", "https://anyone.github.io")
}

// The error names the offending entry, so an operator reading a startup failure
// does not have to bisect the list to find it.
func TestCompileErrorNamesTheEntry(t *testing.T) {
	_, err := Compile([]string{"https://app.swee.net", "nonsense", "http://localhost:*"})
	if err == nil {
		t.Fatal("Compile returned nil error for a list containing a bad entry")
	}
	if got := err.Error(); !contains(got, "nonsense") {
		t.Errorf("error %q does not name the offending entry", got)
	}
}

// Every pattern the documentation and the example config offer must compile.
// This is the test that fails if the two ever drift apart.
func TestDocumentedPatternsCompile(t *testing.T) {
	compile(t,
		"*",
		"https://*.swee.net",
		"http://localhost:*",
		"http://127.0.0.1:*",
		"https://app.swee.net",
		"http://localhost:3000",
		"https://*.swee.net:*",
	)
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// ── Parser edge cases ────────────────────────────────────────────────────────

// The bracketed-IPv6 parser is the fiddliest code here, because the brackets
// change which colon separates the port. Every way of getting that wrong is a
// way of admitting an origin that was not allowlisted, so each is pinned.
func TestMalformedIPLiteralsDenied(t *testing.T) {
	p := compile(t, "http://[::1]:3000", "http://[::1]")
	for _, o := range []string{
		"http://[::1:3000",    // no closing bracket
		"http://[::1]x",       // trailing junk where a port should be
		"http://[::1]:",       // empty port
		"http://[::1]:abc",    //
		"http://[notanip]",    // brackets do not make it an address
		"http://[]",           //
		"http://[::1]:3000:4", // a second colon is not a second port
	} {
		denied(t, p, o)
	}
}

// An IPv6 origin with no port is a distinct origin from one with a port, and
// each matches only the entry that names it.
func TestIPv6OriginWithoutPort(t *testing.T) {
	p := compile(t, "http://[::1]")
	allowed(t, p, "http://[::1]", "http://[::1]")
	denied(t, p, "http://[::1]:3000")
}

// Ports a browser never serialises are refused rather than matched loosely: a
// leading zero and an over-long number are both ways of writing a port that
// some parser somewhere will read differently from this one.
func TestUnusualPortFormsDenied(t *testing.T) {
	p := compile(t, "http://localhost:80", "http://localhost:*")
	for _, o := range []string{
		"http://localhost:0080", // leading zero
		"http://localhost:080",  //
		"http://localhost:0",    // not a real port
		"http://localhost:123456",
		"http://localhost:+80",
		"http://localhost: 80",
	} {
		denied(t, p, o)
	}
}

// A wildcard's parent domain is held to the same rules as any other host, so a
// typo in it is refused at compile time rather than becoming an entry that can
// never match.
func TestCompileRejectsAMalformedWildcardParent(t *testing.T) {
	for _, pattern := range []string{
		"https://*.-swee.net",
		"https://*.swee-.net",
		"https://*.swee .net",
		"https://*..swee.net",
		"https://*.",
	} {
		if _, err := Compile([]string{pattern}); err == nil {
			t.Errorf("Compile(%q) = nil error, want a rejection", pattern)
		}
	}
}
