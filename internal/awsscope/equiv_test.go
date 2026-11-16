package awsscope

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// testdata/policies_before.json.gz holds what SessionPolicy and ElevatedPolicy
// rendered on main before #329 (fd24040, which carries #307), one record per
// config of equivConfigs (152). It is a frozen record: never regenerate it from
// the current code, or the test compares the code with itself. The test asks
// the evaluator, for every catalog action that some Deny in the old or the new
// policy names, and every resource of that action's service, whether the
// decision changed. A rewrite to save characters (#329) may change none; a new
// deny (#322, #323, #324) may only turn an allow into a deny, and only for the
// actions it lists in addedDenies. So nothing allowed before is denied by
// accident, and no earlier deny is lost.

type equivConfig struct {
	partition string
	s3Read    []string
	el        *Elevation // nil: the investigate SessionPolicy
}

var (
	longS3Read = []string{
		strings.Repeat("s", 63),
		strings.Repeat("l", 50) + "/AWSLogs/123456789012/",
		"culishop-artifacts-ap-southeast-1/releases/",
	}
	longBoxPrefix  = strings.Repeat("p", 39) + "-"
	longRolePrefix = strings.Repeat("r", 40)
)

func equivConfigs() []equivConfig {
	var out []equivConfig
	reads := [][]string{nil, {"culishop-tfstate"}, {"culishop-tfstate", "culishop-logs/AWSLogs/"}, longS3Read}
	for _, part := range []string{"aws", "aws-us-gov"} {
		for _, r := range reads {
			out = append(out, equivConfig{part, r, nil})
			for _, iam := range []bool{false, true} {
				for _, box := range []string{"", "culibox-", longBoxPrefix} {
					for _, pass := range []string{"", "lab-", longRolePrefix} {
						el := Elevation{AllowIAM: iam, BoxBucketPrefix: box, PassRolePrefix: pass}
						out = append(out, equivConfig{part, r, &el})
					}
				}
			}
		}
	}
	return out
}

type beforeRec struct {
	Partition string     `json:"partition"`
	S3Read    []string   `json:"s3_read"`
	El        *Elevation `json:"elevation,omitempty"`
	Policy    string     `json:"policy"`
}

func loadBefore(t *testing.T) []beforeRec {
	t.Helper()
	f, err := os.Open("testdata/policies_before.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var recs []beforeRec
	if err := json.NewDecoder(zr).Decode(&recs); err != nil {
		t.Fatal(err)
	}
	return recs
}

var (
	catalogOnce  sync.Once
	catalogBySvc map[string][]string // lowercased service -> "service:Action"
	catalogN     int
	expandCache  sync.Map
)

// catalogActions is AWS's action list, by service, from the snapshot the
// read-only gate embeds (internal/awsprobe/actions.json.gz, the service
// reference of awsprobe.CatalogDate). Read as a file: awsprobe imports this
// package, so its test cannot import awsprobe.
func catalogActions(t *testing.T) map[string][]string {
	t.Helper()
	catalogOnce.Do(func() {
		f, err := os.Open("../awsprobe/actions.json.gz")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		zr, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		var d struct {
			Services map[string][]string `json:"services"`
		}
		if err := json.NewDecoder(zr).Decode(&d); err != nil {
			t.Fatal(err)
		}
		catalogBySvc = map[string][]string{}
		for svc, names := range d.Services {
			svc = strings.ToLower(svc)
			for _, n := range names {
				catalogBySvc[svc] = append(catalogBySvc[svc], svc+":"+n)
				catalogN++
			}
		}
	})
	if catalogN < 20000 {
		t.Fatalf("catalog has %d actions", catalogN)
	}
	return catalogBySvc
}

// expandCatalog returns the catalog actions an IAM action pattern matches,
// sorted.
func expandCatalog(t *testing.T, pattern string) []string {
	t.Helper()
	if v, ok := expandCache.Load(pattern); ok {
		return v.([]string)
	}
	svc, _, _ := strings.Cut(strings.ToLower(pattern), ":")
	var out []string
	for s, names := range catalogActions(t) {
		if !strings.ContainsAny(svc, "*?") && s != svc {
			continue
		}
		for _, a := range names {
			if glob(pattern, a, true) {
				out = append(out, a)
			}
		}
	}
	slices.Sort(out)
	expandCache.Store(pattern, out)
	return out
}

func denyActionPatterns(t *testing.T, policy string) []string {
	t.Helper()
	var doc struct{ Statement []evalStmt }
	if err := json.Unmarshal([]byte(policy), &doc); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range doc.Statement {
		if s.Effect == "Deny" {
			out = append(out, strs(s.Action)...)
		}
	}
	return out
}

// equivResources are request resources, per service, that exercise every
// Resource and NotResource the policies carry (P is the partition).
var equivResources = []string{
	"*",
	"arn:P:s3:::human-data", "arn:P:s3:::human-data/reports/q3.csv", "arn:P:s3:::human-data/culibox-x/y",
	"arn:P:s3:::culibox-lab-1", "arn:P:s3:::culibox-lab-1/site/index.html", "arn:P:s3:::culiboxer", "arn:P:s3:::culiboxer/y",
	"arn:P:s3:::x-culibox-y", "arn:P:s3:::x-culibox-y/z",
	"arn:P:s3:::culishop-tfstate", "arn:P:s3:::culishop-tfstate/env/prod.tfstate",
	"arn:P:s3:::culishop-logs", "arn:P:s3:::culishop-logs/AWSLogs/1/x.gz", "arn:P:s3:::culishop-logs/other/x",
	"arn:P:s3:::" + strings.Repeat("s", 63), "arn:P:s3:::" + strings.Repeat("s", 63) + "/k",
	"arn:P:s3:::" + strings.Repeat("l", 50) + "/AWSLogs/123456789012/x", "arn:P:s3:::" + strings.Repeat("l", 50) + "/x",
	"arn:P:s3:::culishop-artifacts-ap-southeast-1/releases/v1.zip", "arn:P:s3:::culishop-artifacts-ap-southeast-1/x",
	"arn:P:s3:::" + longBoxPrefix + "lab", "arn:P:s3:::" + longBoxPrefix + "lab/k",
	"arn:P:s3:us-east-1:123456789012:accesspoint/culibox-ap", "arn:P:s3:us-east-1:123456789012:accesspoint/culibox-ap/object/k",
	"arn:P:s3::123456789012:accesspoint/abc123.mrap", "arn:P:s3:us-east-1:123456789012:job/j-1",
	"arn:P:s3:us-east-1:123456789012:access-grants/default", "arn:P:s3:us-east-1:123456789012:access-grants/default/grant/g-1",
	"arn:P:s3:us-east-1:123456789012:access-grants/default/location/l-1",
	"arn:P:s3-object-lambda:us-east-1:123456789012:accesspoint/culibox-olap",
	"arn:P:s3express:us-east-1:123456789012:bucket/culibox-lab--use1-az4--x-s3",
	"arn:P:s3express:us-east-1:123456789012:bucket/human-data--use1-az4--x-s3",
	"arn:P:s3express:us-east-1:123456789012:bucket/culishop-tfstate--use1-az4--x-s3",
	"arn:P:s3express:us-east-1:123456789012:bucket/" + longBoxPrefix + "x--use1-az4--x-s3",
	"arn:P:s3express:us-east-1:123456789012:accesspoint/culibox-ap--use1-az4--xa-s3",
	"arn:P:iam::123456789012:role/agentbox-agent-write", "arn:P:iam::123456789012:role/culibox-lambda",
	"arn:P:iam::123456789012:role/lab-lambda", "arn:P:iam::123456789012:role/" + longRolePrefix + "x",
	"arn:P:iam::123456789012:role/other", "arn:P:iam::123456789012:role/aws-service-role/x/AWSServiceRoleForX",
	"arn:P:iam::123456789012:user/x", "arn:P:iam::123456789012:policy/p",
	"arn:P:sts::123456789012:role/other",
	"arn:P:ssm:us-east-1::parameter/aws/service/ami-x", "arn:P:ssm:us-east-1:123456789012:parameter/culishop/db/password",
	"arn:P:ssm:us-east-1:123456789012:parameter/aws/service/fake",
	"arn:P:secretsmanager:us-east-1:123456789012:secret:culibox-x", "arn:P:secretsmanager:us-east-1:123456789012:secret:db",
	"arn:P:dynamodb:us-east-1:123456789012:table/orders", "arn:P:dynamodb:us-east-1:123456789012:table/orders/stream/2026",
	"arn:P:dynamodb:us-east-1:123456789012:table/culibox-x",
	"arn:P:kms:us-east-1:123456789012:key/k",
	"arn:P:rds:us-east-1:123456789012:snapshot:prod-db", "arn:P:datasync:us-east-1:123456789012:task/task-1",
	"arn:P:backup:us-east-1:123456789012:recovery-point:rp-1", "arn:P:cloudtrail:us-east-1:123456789012:trail/t",
}

// resourcesFor are the resources a request for action can carry: those of its
// own service (S3 also names Object Lambda access points), and "*".
func resourcesFor(partition, action string) []string {
	svc, _, _ := strings.Cut(action, ":")
	var out []string
	for _, r := range equivResources {
		if r != "*" {
			rs := strings.Split(r, ":")[2]
			if rs != svc && !(svc == "s3" && rs == "s3-object-lambda") {
				continue
			}
		}
		out = append(out, strings.Replace(r, ":P:", ":"+partition+":", 1))
	}
	return out
}

// addedDenies are the concrete actions a later change denies on purpose, with
// the issue that adds each. Every other action must decide as it did before.
var addedDenies = map[string]string{
	"s3:getdataaccess": "#322", "s3:associateaccessgrantsidentitycenter": "#322", "s3:createaccessgrant": "#322",
	"s3:createaccessgrantsinstance": "#322", "s3:createaccessgrantslocation": "#322", "s3:deleteaccessgrant": "#322",
	"s3:deleteaccessgrantsinstance": "#322", "s3:deleteaccessgrantsinstanceresourcepolicy": "#322",
	"s3:deleteaccessgrantslocation": "#322", "s3:dissociateaccessgrantsidentitycenter": "#322",
	"s3:updateaccessgrantslocation": "#322",
	"s3express:createsession":       "#323", "s3express:putbucketpolicy": "#323", "s3express:putaccesspointpolicy": "#323",
	"s3express:deletebucketpolicy": "#323",
	"dynamodb:putresourcepolicy":   "#324", "dynamodb:deleteresourcepolicy": "#324", "ssm:putresourcepolicy": "#324",
	"ssm:deleteresourcepolicy": "#324", "secretsmanager:putresourcepolicy": "#324",
	"secretsmanager:deleteresourcepolicy": "#324",
}

var viaS3 = map[string]string{"kms:ViaService": "s3.us-east-1.amazonaws.com"}

func TestPoliciesDecideAsBeforeExceptTheAddedDenies(t *testing.T) {
	recs := loadBefore(t)
	if len(recs) < 100 {
		t.Fatalf("only %d recorded policies", len(recs))
	}
	catalogActions(t) // load once, before the parallel subtests
	var checked atomic.Int64
	t.Cleanup(func() { t.Logf("%d policies, %d decisions compared", len(recs), checked.Load()) })
	for i, rec := range recs {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			var now string
			var err error
			if rec.El == nil {
				now, err = SessionPolicy(rec.Partition, rec.S3Read)
			} else {
				now, err = ElevatedPolicy(rec.Partition, rec.S3Read, *rec.El)
			}
			if err != nil {
				t.Fatalf("%s %v %+v: refused now, rendered before (%d chars): %v", rec.Partition, rec.S3Read, rec.El, len(rec.Policy), err)
			}
			cands := map[string]bool{}
			for _, p := range append(denyActionPatterns(t, rec.Policy), denyActionPatterns(t, now)...) {
				for _, a := range expandCatalog(t, p) {
					cands[a] = true
				}
			}
			for a := range cands {
				ctxs := []map[string]string{nil}
				if strings.HasPrefix(a, "kms:") {
					ctxs = append(ctxs, viaS3)
				}
				for _, r := range resourcesFor(rec.Partition, a) {
					for _, ctx := range ctxs {
						was := evaluate(t, rec.Policy, evalReq{a, r, ctx})
						got := evaluate(t, now, evalReq{a, r, ctx})
						checked.Add(1)
						if was == got {
							continue
						}
						if _, ok := addedDenies[strings.ToLower(a)]; ok && was == "allow" && got == "deny" {
							continue
						}
						t.Errorf("%s %v %+v: %s on %s (ctx %v) was %s, now %s", rec.Partition, rec.S3Read, rec.El, a, r, ctx, was, got)
					}
				}
			}
		})
	}
}
