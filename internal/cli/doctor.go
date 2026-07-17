package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/somework/dsx/internal/auth"
	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
)

const enforcedScope = "user:mcp_servers"

const (
	clockSkewWarn = time.Minute
	clockSkewFail = 5 * time.Minute
)

type checkStatus string

const (
	checkOK   checkStatus = "ok"
	checkWarn checkStatus = "warn"
	checkFail checkStatus = "fail"
)

type check struct {
	Name   string      `json:"name"`
	Status checkStatus `json:"status"`
	Detail string      `json:"detail"`
}

func newCheck(name string, st checkStatus, format string, a ...any) check {
	return check{Name: name, Status: st, Detail: fmt.Sprintf(format, a...)}
}

type doctorReport struct {
	Checks []check `json:"checks"`
	OK     bool    `json:"ok"`
}

func (r doctorReport) render(asJSON bool) string {
	if asJSON {
		b, _ := json.Marshal(r)
		return string(b)
	}
	lines := make([]string, 0, len(r.Checks))
	for _, c := range r.Checks {
		lines = append(lines, fmt.Sprintf("%-4s %-12s %s", c.Status, c.Name, c.Detail))
	}
	return strings.Join(lines, "\n")
}

func cmdDoctor(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("doctor")
	asJSON := flags.Bool("json", false, "JSON output")
	if _, err := cmd.ParseArgs(flags, args); err != nil {
		return err
	}

	rep := runDoctor(ctx, c)
	fmt.Println(rep.render(*asJSON))
	if !rep.OK {
		return &dsxerr.Error{Kind: dsxerr.KindFailure, Msg: "doctor found a problem"}
	}
	return nil
}

func runDoctor(ctx context.Context, c *mcp.Client) doctorReport {
	var rep doctorReport

	if t, _ := os.LookupEnv("DSX_TOKEN"); t != "" {
		rep.Checks = append(rep.Checks, newCheck("credentials", checkOK,
			"DSX_TOKEN (scopes and expiry are not knowable from a bare token)"))
	} else {
		creds, src, err := auth.ReadCredentials()
		if err != nil {
			rep.Checks = append(rep.Checks, newCheck("credentials", checkFail, "%v", err))
		} else {
			rep.Checks = append(rep.Checks,
				newCheck("credentials", checkOK, "%s", describeSource(src)),
				tokenExpiryCheck(creds.ExpiresAt),
				scopeCheck(creds.Scopes),
			)
		}
		if src == auth.SrcFile {
			rep.Checks = append(rep.Checks, credentialsModeCheck(auth.CredentialsPath(os.LookupEnv, auth.HomeDir())))
		}
	}

	started := time.Now()
	raw, err := c.ToolsList(ctx)
	latency := time.Since(started)
	if err != nil {
		rep.Checks = append(rep.Checks, newCheck("endpoint", checkFail, "%s: %v", c.Endpoint(), err))
	} else {
		var list struct {
			Tools []struct{} `json:"tools"`
		}
		_ = json.Unmarshal(raw, &list)
		rep.Checks = append(rep.Checks,
			newCheck("endpoint", checkOK, "%s (%d tools, %dms)", c.Endpoint(), len(list.Tools), latency.Milliseconds()),
			clockCheck(c.LastServerDate(), time.Now()),
		)
	}

	rep.OK = true
	for _, ch := range rep.Checks {
		if ch.Status == checkFail {
			rep.OK = false
		}
	}
	return rep
}

func describeSource(src auth.Source) string {
	home := auth.HomeDir()
	switch src {
	case auth.SrcKeychain:
		return "keychain, service " + auth.KeychainServiceName(os.LookupEnv, home)
	case auth.SrcFile:
		return "file " + auth.CredentialsPath(os.LookupEnv, home)
	default:
		return string(src)
	}
}

func tokenExpiryCheck(expiresAtMillis int64) check {
	if expiresAtMillis <= 0 {
		return newCheck("token", checkWarn, "no expiry recorded")
	}
	left := time.Until(time.UnixMilli(expiresAtMillis))
	if left <= 0 {
		return newCheck("token", checkFail,
			"expired %s ago — run any `claude` command to refresh", roundDur(-left))
	}
	return newCheck("token", checkOK, "expires in %s", roundDur(left))
}

func scopeCheck(scopes []string) check {
	for _, s := range scopes {
		if s == enforcedScope {
			return newCheck("scopes", checkOK, "%s", enforcedScope)
		}
	}

	return newCheck("scopes", checkWarn, "%s absent (have: %s) — writes may be refused",
		enforcedScope, strings.Join(scopes, " "))
}

func credentialsModeCheck(path string) check {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return newCheck("permissions", checkOK, "no plaintext credentials file")
		}
		return newCheck("permissions", checkWarn, "%v", err)
	}
	if perm := fi.Mode().Perm(); perm&^fs.FileMode(0o600) != 0 {
		return newCheck("permissions", checkWarn,
			"%s is mode %04o, want 0600 — it holds a bearer token", path, perm)
	}
	return newCheck("permissions", checkOK, "0600")
}

func clockCheck(serverNanos int64, now time.Time) check {
	if serverNanos == 0 {
		return newCheck("clock", checkWarn, "the server sent no Date header; skew unknown")
	}
	skew := now.Sub(time.Unix(0, serverNanos))
	mag := skew
	if mag < 0 {
		mag = -mag
	}
	switch {
	case mag >= clockSkewFail:
		return newCheck("clock", checkFail,
			"local clock is %s from the server's — token expiry will be judged wrongly", roundDur(skew))
	case mag >= clockSkewWarn:
		return newCheck("clock", checkWarn, "local clock is %s from the server's", roundDur(skew))
	default:
		return newCheck("clock", checkOK, "within %s of the server", clockSkewWarn)
	}
}

func roundDur(d time.Duration) time.Duration {
	if d < time.Minute {
		return d.Round(time.Second)
	}
	return d.Round(time.Minute)
}
