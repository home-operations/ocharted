package upstream

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"path"
	"strings"
	"syscall"
	"time"
)

// ErrHostNotAllowed marks a repo host rejected by the allowlist or the
// private-address guard.
var ErrHostNotAllowed = errors.New("upstream host not allowed")

// hostAllowed matches host against the configured allowlist patterns
// (path.Match globs, e.g. "*.github.io"). An empty allowlist allows any host —
// the private-address guard still applies.
func hostAllowed(host string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}
	host = strings.ToLower(host)
	for _, pattern := range allowlist {
		if ok, _ := path.Match(strings.ToLower(pattern), host); ok {
			return true
		}
	}
	return false
}

// newTransport builds the outbound transport. Unless allowPrivate is set, the
// dialer's Control hook rejects private, loopback, and link-local addresses.
// The check runs post-DNS on the concrete address for every connection —
// including redirect targets — so a public hostname resolving to a cluster IP
// (DNS rebinding) is caught at dial time, not at URL-validation time.
func newTransport(allowPrivate bool, timeout time.Duration) *http.Transport {
	dialer := &net.Dialer{Timeout: timeout}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("%w: %q", ErrHostNotAllowed, address)
			}
			ip := net.ParseIP(host)
			if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
				return fmt.Errorf("%w: %s resolves to a non-public address", ErrHostNotAllowed, address)
			}
			return nil
		}
	}
	return &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}
