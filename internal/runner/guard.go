package runner

import (
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/stillright-dev/stillright/internal/mongoquery"
	"github.com/stillright-dev/stillright/internal/scenario"
)

// AllowDatabaseEnv names the variable that lets stillright modify a database
// that does not look disposable. Its value must be "host/database", so one
// approval never covers another database that happens to share a name (every
// Supabase database is called "postgres").
const AllowDatabaseEnv = "STILLRIGHT_ALLOW_DATABASE"

var testNameRe = regexp.MustCompile(`(?i)(^|[_\-.])(test|tests|testing|e2e|ci|tmp|scratch|sandbox)([_\-.]|$)`)

// checkDisposable refuses to let a scenario truncate tables or run setup SQL
// against a database that does not look disposable: not local, not a
// single-label host (a Docker or CI service name), and without a test marker
// in its name. stillright cannot know which database is production, so it
// errs on the side of asking.
func checkDisposable(sc *scenario.Scenario, getenv func(string) string) error {
	_, script := sc.Setup.Script()
	writes := sc.Setup.Reset == scenario.ResetTruncate || strings.TrimSpace(script) != ""
	if !writes {
		return nil
	}
	hosts, db, err := databaseLocation(sc)
	if err != nil {
		return fmt.Errorf("invalid database_url: %v", err)
	}
	local := true
	for _, h := range hosts {
		local = local && isLocalHost(h)
	}
	if local || testNameRe.MatchString(db) {
		return nil
	}
	host := strings.Join(hosts, ",")
	id := host + "/" + db
	if allowed := strings.TrimSpace(getenv(AllowDatabaseEnv)); allowed == id {
		return nil
	}
	action := "runs its setup script"
	if sc.Setup.Reset == scenario.ResetTruncate {
		action = "empties " + strings.Join(sc.Setup.ResetTargets(), ", ")
	}
	return fmt.Errorf("refusing to modify database %q on %s: it does not look disposable (not local, no test "+
		"marker in its name), and this scenario %s before every run. If it really is a throwaway "+
		"database, run with %s=%s", db, host, action, AllowDatabaseEnv, id)
}

// databaseLocation returns the hosts (without ports) and database name a
// scenario's database_url points to, without connecting or resolving DNS.
func databaseLocation(sc *scenario.Scenario) ([]string, string, error) {
	if sc.Target.Database == scenario.MongoDB {
		u, err := mongoquery.ParseURI(sc.Target.DatabaseURL)
		if err != nil {
			return nil, "", err
		}
		var hosts []string
		for _, h := range u.Hosts {
			if name, _, err := net.SplitHostPort(h); err == nil {
				h = name
			}
			hosts = append(hosts, h)
		}
		return hosts, u.Database, nil
	}
	cfg, err := pgx.ParseConfig(sc.Target.DatabaseURL)
	if err != nil {
		return nil, "", err
	}
	return []string{cfg.Host}, cfg.Database, nil
}

// isLocalHost reports whether host is this machine, a Unix socket, or a
// single-label name such as a Docker Compose or CI service ("postgres").
func isLocalHost(host string) bool {
	switch {
	case host == "", strings.HasPrefix(host, "/"), strings.EqualFold(host, "localhost"):
		return true
	case !strings.Contains(host, ".") && !strings.Contains(host, ":"):
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
