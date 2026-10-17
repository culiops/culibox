package egress

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// probeTimeout bounds one whole probe: node start-up plus the 4 s the script
// allows each of its checks. A variable so a test can shorten it.
var probeTimeout = 25 * time.Second

// ExecInput runs `node -e script` in the box with stdin and returns its trimmed
// combined output. The credential travels in stdin, never in argv or env.
type ExecInput func(ctx context.Context, script, stdin string) (string, error)

// ProbeTarget says where the box finds the proxy and what the probe tries.
type ProbeTarget struct {
	ProxyHost string // what the box dials: the gateway IP (Linux) or "host.lima.internal" (darwin)
	Port      int
	DenyHost  string // "" → "example.com"
	Direct    string // "" → "1.1.1.1:443"
}

const (
	defaultDenyHost = "example.com"
	defaultDirect   = "1.1.1.1:443"
	failPrefix      = "egress check failed: "
)

var (
	reDirect = regexp.MustCompile(`^DIRECT (REACHED|BLOCKED [A-Z0-9_]{1,24})$`)
	reDNS    = regexp.MustCompile(`^DNS (RESOLVED|BLOCKED [A-Z0-9_]{1,24})$`)
	reAllow  = regexp.MustCompile(`^ALLOW (\d{3}|ERR [A-Z0-9_]{1,24})$`)
	reDeny   = regexp.MustCompile(`^DENY (\d{3}|ERR [A-Z0-9_]{1,24})$`)
)

// Probe runs the four checks of spec §5.6 inside the box. It passes only when
// the script printed exactly DIRECT BLOCKED, DNS BLOCKED, ALLOW 200 and DENY 403
// AND the proxy itself saw the probe principal open exactly one allowed tunnel
// and refuse exactly one CONNECT. Everything else, including a script that
// could not run, fails.
//
// The principal is single-use: the allowed slot is spent by the ALLOW leg even
// when the proxy was busy or the upstream down, so Probe never retries; the
// caller starts a fresh Probe. Neither the user nor the password appears in any
// returned error.
func Probe(ctx context.Context, p *Proxy, box string, t ProbeTarget, exec ExecInput) error {
	if t.ProxyHost == "" || t.Port <= 0 || t.Port > 65535 {
		return fmt.Errorf("%sno usable proxy address (%q, port %d)", failPrefix, t.ProxyHost, t.Port)
	}
	if t.DenyHost == "" {
		t.DenyHost = defaultDenyHost
	}
	if t.Direct == "" {
		t.Direct = defaultDirect
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	user, pass, done := p.ProbeCredential(box)
	defer done() // the principal must not linger until the next sweep
	secrets := []string{pass, user, base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))}
	scrub := func(s string) string {
		for _, x := range secrets {
			s = strings.ReplaceAll(s, x, "[redacted]")
		}
		if len(s) > 300 {
			s = s[:300] + "..."
		}
		return s
	}

	in, err := json.Marshal(map[string]any{
		"user": user, "pass": pass, "host": t.ProxyHost, "port": t.Port,
		"allow": p.o.ProbeHost, "deny": t.DenyHost, "direct": t.Direct,
	})
	if err != nil {
		return fmt.Errorf("%scannot build the probe input", failPrefix)
	}
	out, err := exec(ctx, probeScript(), string(in)+"\n")
	if err != nil {
		return fmt.Errorf("%sthe probe could not run in the box: %s", failPrefix, scrub(err.Error()))
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%sthe probe did not finish in time: %w", failPrefix, err)
	}

	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(out), "\r\n", "\n"), "\n")
	if len(lines) != 4 || !reDirect.MatchString(lines[0]) || !reDNS.MatchString(lines[1]) ||
		!reAllow.MatchString(lines[2]) || !reDeny.MatchString(lines[3]) {
		// The output is not echoed: it is whatever the box printed.
		return fmt.Errorf("%sunexpected probe output (%d lines)", failPrefix, len(lines))
	}
	direct, dns, allow, deny := lines[0], lines[1], strings.TrimPrefix(lines[2], "ALLOW "), strings.TrimPrefix(lines[3], "DENY ")

	// Containment first: a box that can get out outranks a proxy that is busy.
	if direct == "DIRECT REACHED" {
		return fmt.Errorf("%sthe box can reach %s directly; its network is not the egress network", failPrefix, t.Direct)
	}
	if dns == "DNS RESOLVED" {
		return fmt.Errorf("%sthe box can resolve external names (%s); DNS is not blocked", failPrefix, t.DenyHost)
	}
	if deny == "200" {
		return fmt.Errorf("%sthe proxy opened a tunnel to %s, which is not allowlisted", failPrefix, t.DenyHost)
	}
	switch {
	case allow == "200":
	case allow == "502" || allow == "503" || allow == "504":
		return fmt.Errorf("%segress proxy busy / upstream unreachable (status %s for %s); retry shortly", failPrefix, allow, p.o.ProbeHost)
	case allow == "407":
		return fmt.Errorf("%sthe egress proxy rejected the probe credential (expired or already used)", failPrefix)
	case allow == "403":
		return fmt.Errorf("%sthe egress proxy refused the probe host %s", failPrefix, p.o.ProbeHost)
	case strings.HasPrefix(allow, "ERR "):
		return fmt.Errorf("%sthe box cannot reach the egress proxy at %s:%d (%s)", failPrefix, t.ProxyHost, t.Port, strings.TrimPrefix(allow, "ERR "))
	default:
		return fmt.Errorf("%sthe egress proxy answered %s for the allowed host, expected 200", failPrefix, allow)
	}
	switch {
	case deny == "403":
	case strings.HasPrefix(deny, "ERR "):
		return fmt.Errorf("%sthe box cannot reach the egress proxy at %s:%d (%s)", failPrefix, t.ProxyHost, t.Port, strings.TrimPrefix(deny, "ERR "))
	default:
		return fmt.Errorf("%sthe egress proxy answered %s for the non-allowlisted host, expected 403", failPrefix, deny)
	}

	// The proxy's own counters decide. The allowed count lands just after the
	// 200 is written, so give it a moment before reading.
	want := ProbeResult{Allowed: 1, Denied: 1}
	for end := time.Now().Add(500 * time.Millisecond); p.probeSeen(user) != want && time.Now().Before(end); {
		time.Sleep(5 * time.Millisecond)
	}
	if res := done(); res != want {
		return fmt.Errorf("%sthe egress proxy did not see the probe's checks (allowed %d, denied %d)", failPrefix, res.Allowed, res.Denied)
	}
	return nil
}

// probeScript reads one JSON line {user,pass,host,port,allow,deny,direct} from
// stdin, runs the four checks concurrently (4 s each), and prints exactly four
// lines. It prints no input value: not the user, the password, the proxy
// address, nor anything the proxy said (only a three-digit status or an
// upper-case error code).
func probeScript() string {
	return `const net=require('net'),dns=require('dns');
const T=4000;
const code=e=>{const c=e&&e.code;return typeof c==='string'&&/^[A-Z0-9_]{1,24}$/.test(c)?c:'EUNKNOWN'};
const out=(s,n)=>process.stdout.write(s,()=>process.exit(n));
let raw='',started=false;
process.stdin.setEncoding('utf8');
process.stdin.on('data',d=>{raw+=d;if(raw.indexOf('\n')>=0)go()});
process.stdin.on('end',go);
function go(){
if(started)return;started=true;
let c;
try{c=JSON.parse(raw.split('\n')[0]);if(!c||typeof c.host!=='string'||typeof c.direct!=='string')throw 0}catch(e){return out('PROBE ERR\n',2)}
const auth=Buffer.from(c.user+':'+c.pass).toString('base64');
const direct=()=>new Promise(res=>{
let d=false,s;
const fin=v=>{if(!d){d=true;if(s)s.destroy();res(v)}};
try{
const i=c.direct.lastIndexOf(':');
s=net.connect({host:c.direct.slice(0,i),port:+c.direct.slice(i+1)});
s.setTimeout(T,()=>fin('DIRECT BLOCKED ETIMEDOUT'));
s.on('connect',()=>fin('DIRECT REACHED'));
s.on('error',e=>fin('DIRECT BLOCKED '+code(e)));
}catch(e){fin('DIRECT BLOCKED '+code(e))}
});
const look=()=>new Promise(res=>{
let d=false;
const t=setTimeout(()=>fin('DNS BLOCKED ETIMEDOUT'),T);
const fin=v=>{if(!d){d=true;clearTimeout(t);res(v)}};
try{dns.lookup(c.deny,e=>fin(e?'DNS BLOCKED '+code(e):'DNS RESOLVED'))}catch(e){fin('DNS BLOCKED '+code(e))}
});
const via=(label,h)=>new Promise(res=>{
let d=false,s,buf='';
const fin=v=>{if(!d){d=true;if(s)s.destroy();res(label+' '+v)}};
try{
s=net.connect({host:c.host,port:+c.port});
s.setTimeout(T,()=>fin('ERR ETIMEDOUT'));
s.on('error',e=>fin('ERR '+code(e)));
s.on('close',()=>fin('ERR ECLOSED'));
s.on('connect',()=>s.write('CONNECT '+h+':443 HTTP/1.1\r\nHost: '+h+':443\r\nProxy-Authorization: Basic '+auth+'\r\n\r\n'));
s.on('data',x=>{
buf+=x.toString('latin1');
const i=buf.indexOf('\r\n');
if(i>=0){const f=buf.slice(0,i).split(' ');fin(/^[0-9]{3}$/.test(f[1]||'')?f[1]:'ERR EBADREPLY')}
});
}catch(e){fin('ERR '+code(e))}
});
Promise.all([direct(),look(),via('ALLOW',c.allow),via('DENY',c.deny)]).then(r=>out(r.join('\n')+'\n',0));
}
`
}
