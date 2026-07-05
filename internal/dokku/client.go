// Package dokku implements a minimal SSH client for driving the Dokku CLI
// over its forced-command SSH interface (ssh dokku@host <command> <args...>).
package dokku

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Client runs Dokku CLI commands against a remote host over SSH.
//
// Commands are serialized through mu: several Dokku commands (ports:add,
// domains:add, scheduler:set, storage:mount, ...) regenerate the app's proxy
// config on the remote host as a side effect, and concurrent invocations for
// the same app race on those files (observed in practice as
// "mv: cannot create regular file '.../nginx.conf': File exists"). Dokku's
// CLI is not designed to be driven concurrently, so the provider runs one
// command at a time regardless of Terraform's own parallelism.
type Client struct {
	host      string
	port      string
	user      string
	signer    ssh.Signer
	hostKeyCB ssh.HostKeyCallback
	timeout   time.Duration
	mu        sync.Mutex
}

// Config holds the parameters needed to dial a Dokku host.
type Config struct {
	Host                  string
	Port                  string
	User                  string
	PrivateKeyPEM         string
	InsecureIgnoreHostKey bool
	Timeout               time.Duration
}

func NewClient(cfg Config) (*Client, error) {
	if cfg.Port == "" {
		cfg.Port = "22"
	}
	if cfg.User == "" {
		cfg.User = "dokku"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	signer, err := ssh.ParsePrivateKey([]byte(cfg.PrivateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("parsing private key: %w", err)
	}

	hostKeyCB := ssh.InsecureIgnoreHostKey()

	return &Client{
		host:      cfg.Host,
		port:      cfg.Port,
		user:      cfg.User,
		signer:    signer,
		hostKeyCB: hostKeyCB,
		timeout:   cfg.Timeout,
	}, nil
}

// Result captures the outcome of a single dokku command invocation.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// joinArgs builds the raw command string sent as the SSH "exec" payload.
// Dokku's forced-command wrapper splits $SSH_ORIGINAL_COMMAND on whitespace
// via unquoted shell expansion (e.g. `set -- $SSH_ORIGINAL_COMMAND`), which
// does field-splitting but no quote removal. That means quoting arguments
// ourselves would leak literal quote characters into argv, so arguments are
// joined as-is; in exchange no argument may contain whitespace (matching
// the real-world constraints of `ssh dokku@host <command>` usage).
func joinArgs(args []string) string {
	return strings.Join(args, " ")
}

func (c *Client) dial() (*ssh.Client, error) {
	config := &ssh.ClientConfig{
		User: c.user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(c.signer),
		},
		HostKeyCallback: c.hostKeyCB,
		Timeout:         c.timeout,
	}

	addr := net.JoinHostPort(c.host, c.port)
	return ssh.Dial("tcp", addr, config)
}

// Run executes a single dokku subcommand (e.g. "apps:create", "myapp") and
// returns its combined result. Each call opens a fresh SSH session, matching
// how the Dokku forced-command interface expects to be driven.
func (c *Client) Run(args ...string) (*Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	conn, err := c.dial()
	if err != nil {
		return nil, fmt.Errorf("dialing %s@%s:%s: %w", c.user, c.host, c.port, err)
	}
	defer conn.Close()

	session, err := conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("opening ssh session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	cmd := joinArgs(args)
	exitCode := 0
	if err := session.Run(cmd); err != nil {
		if exitErr, ok := err.(*ssh.ExitError); ok {
			exitCode = exitErr.ExitStatus()
		} else {
			return nil, fmt.Errorf("running %q: %w", cmd, err)
		}
	}

	res := &Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}
	return res, nil
}

// RunChecked is like Run but returns an error if the remote command exited
// non-zero, including captured stdout/stderr in the error message.
func (c *Client) RunChecked(args ...string) (*Result, error) {
	res, err := c.Run(args...)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return res, fmt.Errorf("dokku %s: exit %d: %s%s", strings.Join(args, " "), res.ExitCode, res.Stderr, res.Stdout)
	}
	return res, nil
}

// Report runs "<resource>:report <name> --format json" (or, when name is
// empty, "<resource>:report --format json" for global reports) and decodes
// the resulting key/value pairs. Dokku's report commands consistently
// support --format json across plugins.
func (c *Client) Report(resource, name string) (map[string]string, error) {
	args := []string{resource + ":report"}
	if name != "" {
		args = append(args, name)
	}
	args = append(args, "--format", "json")

	res, err := c.Run(args...)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, &NotFoundError{Resource: resource, Name: name, Stderr: res.Stderr}
	}

	// Decoded via map[string]interface{} and stringified rather than
	// map[string]string directly: it consistently holds for :report
	// commands, but a stray non-string value would otherwise fail the
	// whole document instead of just that field.
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(res.Stdout), &raw); err != nil {
		return nil, fmt.Errorf("parsing %s:report json: %w (output: %s)", resource, err, res.Stdout)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		} else {
			out[k] = fmt.Sprint(v)
		}
	}
	return out, nil
}

// NotFoundError indicates a report/info lookup failed, most often because
// the underlying app/service/entry no longer exists on the remote host.
type NotFoundError struct {
	Resource string
	Name     string
	Stderr   string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s %q: %s", e.Resource, e.Name, e.Stderr)
}
