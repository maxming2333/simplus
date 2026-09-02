package atremote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/leonfox28/simplus/internal/attransport"
)

const (
	sessionPath  = "/at/session"
	commandPath  = "/at/command"
	exchangePath = "/at/exchange"

	defaultRequestTimeout = 20 * time.Second
	minimumRequestTimeout = time.Second
	maximumRequestTimeout = 120 * time.Second

	maximumTokenLength = 128
)

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,128}$`)

// Target is one reviewed remote AT bridge. It is an internal deployment fact
// assembled in cmd/simplus-agent from a private configuration file; no Web, API
// or application caller can supply or observe one.
type Target struct {
	Key            string
	Username       string
	Password       string
	RequestTimeout time.Duration

	baseURL   string
	plaintext bool
}

// BaseURLHost returns only the host part of the configured base URL, for
// startup logging. The scheme, path and credentials are withheld so ordinary
// logs never carry a bridge credential.
func (target Target) BaseURLHost() string {
	parsed, err := url.Parse(target.baseURL)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// Plaintext reports whether the bridge is reached without TLS, so the assembly
// point can warn once at startup.
func (target Target) Plaintext() bool { return target.plaintext }

// NewTarget validates one bridge definition. Validation is strict and
// fail-closed: an unusable target must be rejected at assembly time rather than
// producing a confusing transport failure during the first probe.
func NewTarget(key, baseURL, username, password string, requestTimeout time.Duration) (Target, error) {
	if !ValidKey(key) {
		return Target{}, fmt.Errorf("remote AT bridge key %q is invalid", key)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return Target{}, fmt.Errorf("remote AT bridge %q has an unparsable base URL", key)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Target{}, fmt.Errorf("remote AT bridge %q base URL scheme must be http or https", key)
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Target{}, fmt.Errorf("remote AT bridge %q base URL must carry only a scheme, host and path", key)
	}
	if strings.Contains(username, ":") {
		return Target{}, fmt.Errorf("remote AT bridge %q username must not contain a colon", key)
	}
	if (username == "") != (password == "") {
		return Target{}, fmt.Errorf("remote AT bridge %q must set both or neither of username and password", key)
	}
	timeout := requestTimeout
	if timeout == 0 {
		timeout = defaultRequestTimeout
	}
	if timeout < minimumRequestTimeout || timeout > maximumRequestTimeout {
		return Target{}, fmt.Errorf("remote AT bridge %q request timeout must be from 1s through 2m", key)
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	return Target{
		Key: key, Username: username, Password: password, RequestTimeout: timeout,
		baseURL: parsed.Scheme + "://" + parsed.Host + parsed.Path, plaintext: parsed.Scheme == "http",
	}, nil
}

// Opener resolves bridge control-endpoint locators to exclusive bridge
// sessions. It serves only keys present in its reviewed target table.
type Opener struct {
	targets map[string]Target
	client  *http.Client
}

var _ attransport.Opener = (*Opener)(nil)

// NewOpener builds the bridge opener. It performs no I/O, matching the
// construction contract of the existing tty transports so a misconfigured
// deployment fails at validation rather than at process start.
func NewOpener(targets []Target) (*Opener, error) {
	if len(targets) == 0 {
		return nil, errors.New("remote AT opener requires at least one bridge target")
	}
	table := make(map[string]Target, len(targets))
	longest := minimumRequestTimeout
	for _, target := range targets {
		if target.baseURL == "" || !ValidKey(target.Key) {
			return nil, errors.New("remote AT bridge target was not built by NewTarget")
		}
		if _, exists := table[target.Key]; exists {
			return nil, fmt.Errorf("duplicate remote AT bridge key %q", target.Key)
		}
		table[target.Key] = target
		if target.RequestTimeout > longest {
			longest = target.RequestTimeout
		}
	}
	return &Opener{
		targets: table,
		client: &http.Client{
			// Per-request deadlines come from the caller's context. The client
			// timeout is only a backstop above the longest bounded query.
			Timeout: longest + maximumQueryTimeout + bridgeGrace,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("remote AT bridge must not redirect")
			},
		},
	}, nil
}

type openResponse struct {
	Session     string `json:"session"`
	ExpiresInMS int64  `json:"expiresInMs"`
}

// Open claims the bridge's modem UART for one conversation. Every failure is
// classified into an existing attransport.OpenError kind so hardwareprobe's
// probeOpenFailure renders the same typed probe errors as the local path.
func (opener *Opener) Open(endpoint string) (attransport.Session, error) {
	if opener == nil || len(opener.targets) == 0 {
		return nil, attransport.NewOpenError(attransport.OpenUnsupported, false, errors.New("remote AT transport is not configured"))
	}
	key, ok := ParseLocator(endpoint)
	if !ok {
		return nil, attransport.NewOpenError(attransport.OpenUnavailable, false, errors.New("remote AT endpoint locator is invalid"))
	}
	target, ok := opener.targets[key]
	if !ok {
		return nil, attransport.NewOpenError(attransport.OpenUnavailable, false, errors.New("remote AT endpoint is not a reviewed bridge"))
	}
	current := &session{client: opener.client, target: target, timeout: target.RequestTimeout}
	ctx, cancel := context.WithTimeout(context.Background(), target.RequestTimeout)
	defer cancel()
	body, status, err := current.do(ctx, http.MethodPost, sessionPath, []byte("{}"))
	defer zero(body)
	if err != nil {
		if errors.Is(err, ErrResponseOversize) {
			return nil, attransport.NewOpenError(attransport.OpenUnavailable, false, errors.New("remote AT bridge returned an oversize response"))
		}
		return nil, attransport.NewOpenError(attransport.OpenUnavailable, true, errors.New("remote AT bridge is unreachable"))
	}
	if failure := classifyOpenStatus(status); failure != nil {
		return nil, failure
	}
	var decoded openResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&decoded); decodeErr != nil {
		return nil, attransport.NewOpenError(attransport.OpenConfigure, true, errors.New("remote AT bridge session response is malformed"))
	}
	if len(decoded.Session) > maximumTokenLength || !tokenPattern.MatchString(decoded.Session) {
		return nil, attransport.NewOpenError(attransport.OpenConfigure, true, errors.New("remote AT bridge session token is invalid"))
	}
	current.token = decoded.Session
	return current, nil
}

// classifyOpenStatus maps bridge status classes onto the fixed OpenError kinds.
// Unlisted statuses are treated as an unavailable bridge rather than being
// guessed into a more permissive class.
func classifyOpenStatus(status int) *attransport.OpenError {
	switch status {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusConflict, http.StatusLocked, http.StatusTooManyRequests:
		return attransport.NewOpenError(attransport.OpenBusy, true, errors.New("remote AT bridge is already in use"))
	case http.StatusUnauthorized, http.StatusForbidden:
		return attransport.NewOpenError(attransport.OpenPermission, false, errors.New("remote AT bridge rejected the agent credential"))
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return attransport.NewOpenError(attransport.OpenConfigure, true, errors.New("remote AT bridge refused the session request"))
	case http.StatusNotFound, http.StatusNotImplemented, http.StatusMethodNotAllowed:
		return attransport.NewOpenError(attransport.OpenUnsupported, false, errors.New("remote AT bridge does not implement the session contract"))
	default:
		return attransport.NewOpenError(attransport.OpenUnavailable, true, errors.New("remote AT bridge failed to open a session"))
	}
}
