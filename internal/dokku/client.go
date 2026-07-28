// Package dokku implements a minimal SSH client for driving the Dokku CLI
// over its forced-command SSH interface (ssh dokku@host <command> <args...>).
package dokku

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
	"golang.org/x/crypto/ssh"
)

// maxPoolConnections caps the number of concurrent SSH connections Client
// will hold open at once. Terraform doesn't communicate its -parallelism
// value to providers over the plugin protocol, so this can't be read at
// runtime; 10 matches Terraform's own default -parallelism.
const maxPoolConnections = 10

// Client runs Dokku CLI commands against a remote host over SSH.
//
// Commands run concurrently over an elastic, capped pool of SSH connections
// (see acquire/release): a call reuses an idle connection when one's
// available, dials a fresh one while the pool has spare capacity, or blocks
// until a connection is released once the cap is reached. There is no
// serialization beyond that cap. Several Dokku commands (ports:add,
// domains:add, scheduler:set, storage:mount, ...) regenerate the app's proxy
// config on the remote host as a side effect, and concurrent invocations for
// the same app can race on those files (observed in practice as "mv: cannot
// create regular file '.../nginx.conf': File exists" before this pool
// replaced a single global mutex). Allowing that risk in exchange for
// letting Terraform's own parallelism actually run applies concurrently was
// a deliberate tradeoff, not an oversight.
type Client struct {
	host      string
	port      string
	user      string
	signer    ssh.Signer
	hostKeyCB ssh.HostKeyCallback
	timeout   time.Duration

	// slots bounds the pool to maxPoolConnections: each entry is either an
	// idle, reusable *ssh.Client or nil (a free slot to dial a new one
	// into). acquire blocks on this channel when the pool is at capacity.
	slots chan *ssh.Client
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

	slots := make(chan *ssh.Client, maxPoolConnections)
	for range maxPoolConnections {
		slots <- nil
	}

	return &Client{
		host:      cfg.Host,
		port:      cfg.Port,
		user:      cfg.User,
		signer:    signer,
		hostKeyCB: hostKeyCB,
		timeout:   cfg.Timeout,
		slots:     slots,
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

// acquire reserves one of the pool's maxPoolConnections slots, blocking if
// all are currently checked out. It returns a ready-to-use connection: a
// pooled idle one when the slot held one, or a freshly dialed one otherwise
// (fresh reports which, since a newly dialed connection failing to open a
// session is a real error rather than the staleness Run retries around).
func (c *Client) acquire() (conn *ssh.Client, fresh bool, err error) {
	slot := <-c.slots
	if slot != nil {
		return slot, false, nil
	}
	conn, err = c.dial()
	if err != nil {
		c.slots <- nil
		return nil, false, err
	}
	return conn, true, nil
}

// release returns a slot to the pool: conn is kept for reuse by the next
// acquire, or nil if it was closed because it turned out to be broken.
func (c *Client) release(conn *ssh.Client) {
	c.slots <- conn
}

// Run executes a single dokku subcommand (e.g. "apps:create", "myapp") and
// returns its combined result. Each call checks out a connection from the
// pool (see acquire) and opens a fresh SSH session on it, matching how the
// Dokku forced-command interface expects to be driven.
//
// The command is logged at debug level (via tflog, so it only surfaces with
// TF_LOG=DEBUG or lower) before it's sent; the response is never logged,
// since Dokku output can include secrets (env vars, registry credentials).
func (c *Client) Run(ctx context.Context, args ...string) (*Result, error) {
	cmd := joinArgs(args)
	tflog.Debug(ctx, "sending dokku command", map[string]any{"command": cmd})

	conn, fresh, err := c.acquire()
	if err != nil {
		return nil, fmt.Errorf("dialing %s@%s:%s: %w", c.user, c.host, c.port, err)
	}

	session, err := conn.NewSession()
	if err != nil && !fresh {
		// Pooled connection died (e.g. server-side idle timeout); discard it
		// and retry once against a freshly dialed one.
		conn.Close()
		conn, err = c.dial()
		if err == nil {
			session, err = conn.NewSession()
		}
	}
	if err != nil {
		c.release(nil)
		return nil, fmt.Errorf("opening ssh session: %w", err)
	}

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	exitCode := 0
	runErr := session.Run(cmd)
	session.Close()

	if runErr != nil {
		if exitErr, ok := runErr.(*ssh.ExitError); ok {
			exitCode = exitErr.ExitStatus()
			c.release(conn)
		} else {
			conn.Close()
			c.release(nil)
			return nil, fmt.Errorf("running %q: %w", cmd, runErr)
		}
	} else {
		c.release(conn)
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
func (c *Client) RunChecked(ctx context.Context, args ...string) (*Result, error) {
	res, err := c.Run(ctx, args...)
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
func (c *Client) Report(ctx context.Context, resource, name string) (map[string]string, error) {
	args := []string{resource + ":report"}
	if name != "" {
		args = append(args, name)
	}
	args = append(args, "--format", "json")

	res, err := c.Run(ctx, args...)
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
