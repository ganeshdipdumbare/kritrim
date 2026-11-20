package kritrim

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Endpoint is an egress rule that narrows a host to the ports, methods and
// paths a request may use. A request matches when every listed field
// matches; an empty field matches only its default (Ports) or anything
// (Methods, Paths).
//
// Endpoints sit beside the plain host lists (HTTPConfig.AllowHosts,
// Sealed.Egress), which still allow any method and path on their hosts.
// Prefer Endpoints for anything that changes state.
type Endpoint struct {
	// Host is an exact host name or a "*.example.com" suffix. Exact hosts
	// may be private addresses; suffix hosts must resolve to public ones.
	Host string
	// Ports lists the ports allowed. Empty means the scheme's default port
	// only (443 for https, 80 for http).
	Ports []int
	// Methods lists the HTTP methods allowed, e.g. "GET". Empty allows any
	// method the broker supports.
	Methods []string
	// Paths lists path patterns. "*" matches one path segment and "**"
	// matches any number of segments, e.g. "/repos/*/issues/**". Empty
	// allows any path.
	Paths []string
	// Audit lets requests that match Host but not Ports, Methods or Paths
	// through, and logs them (Config.Logger) instead of refusing them. Use
	// it to learn what a policy needs before enforcing it.
	Audit bool
}

func (e Endpoint) validate() error {
	h := strings.ToLower(strings.TrimSpace(e.Host))
	if h == "" || h == "*" || h == "*." || strings.ContainsAny(h, "/:@ ") {
		return fmt.Errorf("kritrim: endpoint host %q must be a host name or a *.suffix", e.Host)
	}
	if strings.Contains(strings.TrimPrefix(h, "*."), "*") {
		return fmt.Errorf("kritrim: endpoint host %q may use * only as a leading *. label", e.Host)
	}
	for _, p := range e.Ports {
		if p < 1 || p > 65535 {
			return fmt.Errorf("kritrim: endpoint %s: port %d is out of range", e.Host, p)
		}
	}
	for _, m := range e.Methods {
		if !validMethod(strings.ToUpper(m)) {
			return fmt.Errorf("kritrim: endpoint %s: method %q is not supported", e.Host, m)
		}
	}
	for _, p := range e.Paths {
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("kritrim: endpoint %s: path %q must start with /", e.Host, p)
		}
	}
	return nil
}

func validateEndpoints(eps []Endpoint) error {
	var errs []error
	for _, e := range eps {
		errs = append(errs, e.validate())
	}
	return errors.Join(errs...)
}

// endpointHosts returns the host part of each endpoint, for the dial-time
// checks that only care whether a host is exact or a suffix.
func endpointHosts(eps []Endpoint) []string {
	out := make([]string, 0, len(eps))
	for _, e := range eps {
		out = append(out, e.Host)
	}
	return out
}

// egressMatch is the outcome of checking a request against Endpoints.
type egressMatch struct {
	ok    bool // a rule allows the request
	exact bool // the allowing rule names the host exactly
	audit bool // allowed only because the rule is in audit mode
	why   string
}

// matchEndpoints checks one request. It returns the first rule that allows
// it in full; failing that, an audit-mode rule for the host; failing that,
// the reason the closest rule refused it.
func matchEndpoints(method string, u *url.URL, eps []Endpoint) egressMatch {
	host := strings.ToLower(u.Hostname())
	port := urlPort(u)
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	var audit *egressMatch
	why := ""
	for _, e := range eps {
		exact, ok := hostAllowed(host, []string{e.Host})
		if !ok {
			continue
		}
		reason := ""
		switch {
		case !portAllowed(port, u.Scheme, e.Ports):
			reason = fmt.Sprintf("port %d is not allowed for %s", port, e.Host)
		case len(e.Methods) > 0 && !containsFold(e.Methods, method):
			reason = fmt.Sprintf("method %s is not allowed for %s", method, e.Host)
		case len(e.Paths) > 0 && !pathAllowed(path, e.Paths):
			reason = fmt.Sprintf("path %s is not allowed for %s", path, e.Host)
		}
		if reason == "" {
			return egressMatch{ok: true, exact: exact}
		}
		if e.Audit && audit == nil {
			audit = &egressMatch{ok: true, exact: exact, audit: true, why: reason}
		}
		if why == "" {
			why = reason
		}
	}
	if audit != nil {
		return *audit
	}
	return egressMatch{why: why}
}

func urlPort(u *url.URL) int {
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return -1
		}
		return n
	}
	if strings.EqualFold(u.Scheme, "http") {
		return 80
	}
	return 443
}

func portAllowed(port int, scheme string, ports []int) bool {
	if len(ports) == 0 {
		if strings.EqualFold(scheme, "http") {
			return port == 80
		}
		return port == 443
	}
	for _, p := range ports {
		if p == port {
			return true
		}
	}
	return false
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

// pathAllowed matches an escaped request path against the patterns. The
// path is compared segment by segment, so "%2F" inside a segment never
// counts as a separator, and "." or ".." segments are refused outright:
// a server would resolve them to a path the pattern did not approve.
func pathAllowed(p string, patterns []string) bool {
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for _, s := range segs {
		if dec, err := url.PathUnescape(s); err != nil || dec == "." || dec == ".." {
			return false
		}
	}
	for _, pat := range patterns {
		if globSegs(strings.Split(strings.TrimPrefix(pat, "/"), "/"), segs) {
			return true
		}
	}
	return false
}

func globSegs(pat, segs []string) bool {
	for len(pat) > 0 {
		switch pat[0] {
		case "**":
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(segs); i++ {
				if globSegs(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		case "*":
			if len(segs) == 0 || segs[0] == "" {
				return false
			}
		default:
			if len(segs) == 0 || segs[0] != pat[0] {
				return false
			}
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// hostPort is host:port for log lines.
func hostPort(u *url.URL) string {
	return net.JoinHostPort(u.Hostname(), strconv.Itoa(urlPort(u)))
}
