package awsprobe

import "strings"

// readVerbs are the verb prefixes that pass the gate on their own. Matching
// ignores case, so apigateway:GET is a read. Credential-minting reads (for
// example ec2:GetPasswordData) are screened separately by the gate.
var readVerbs = []string{"get", "list", "describe", "batchget", "batchdescribe", "search", "lookup", "view",
	"query", "scan", "select"}

// isReadVerb reports whether pattern names one service and its literal verb
// (the part before any * or ?) starts with a read verb. A wildcard in the
// service name, or an empty literal verb, is never a read.
func isReadVerb(pattern string) bool {
	svc, verb, ok := strings.Cut(literal(strings.ToLower(pattern)), ":")
	if !ok || svc == "" {
		return false
	}
	for _, v := range readVerbs {
		if strings.HasPrefix(verb, v) {
			return true
		}
	}
	return false
}

// VerbEntry is one reviewed non-read-verb pattern. An empty SideEffect means
// benign. Otherwise SideEffect lists the concrete actions the simulation must
// show denied before the permission set passes.
type VerbEntry struct {
	SideEffect []string
	Reason     string
}

// The benign rule (founder, 2026-10-30): an action is a side-effect if it
// changes stored data or configuration, mints or returns credentials, or sends
// data outside AWS. Otherwise it is benign. Every Reason starts with the clause
// of the rule it rests on.
const (
	ruleBenign  = "benign rule: benign (changes no stored data or config, mints or returns no credentials, sends no data outside AWS)"
	ruleChanges = "benign rule: side-effect (changes stored data or config)"
	ruleCreds   = "benign rule: side-effect (mints or returns credentials)"
	ruleSends   = "benign rule: side-effect (sends data outside AWS)"
	ruleUnknown = "benign rule: side-effect (effect undocumented, so assumed to change stored data or config)"
)

func benign(why string) VerbEntry { return VerbEntry{Reason: ruleBenign + "; " + why} }

func sideEffect(rule, why string, actions ...string) VerbEntry {
	return VerbEntry{SideEffect: actions, Reason: rule + "; " + why}
}

// NonStandard is the reviewed table of non-read-verb patterns, keyed by the
// lowercased pattern. It covers every non-read pattern in the AWS managed
// policies ReadOnlyAccess (v190) and ViewOnlyAccess (v47); a pattern missing
// from it refuses. Side-effect actions are the pattern's expansion in the AWS
// service reference (2026-10-30).
var NonStandard = lowerKeys(map[string]VerbEntry{
	"access-analyzer:ValidatePolicy":                            benign("checks a policy document and returns findings"),
	"agent-registry:InvokeRegistryMcp":                          sideEffect(ruleUnknown, "invokes a registry's MCP server, new service with no documented limits", "agent-registry:InvokeRegistryMcp"),
	"aiops:ValidateInvestigationGroup":                          benign("checks an investigation group's settings"),
	"appsync:EvaluateMappingTemplate":                           benign("evaluates a template against a supplied context and returns the result"),
	"aps:PreviewAnomalyDetector":                                benign("previews detector output without creating one"),
	"athena:Batch*":                                             benign("expands to BatchGetNamedQuery, BatchGetPreparedStatement, BatchGetQueryExecution only"),
	"auditmanager:ValidateAssessmentReportIntegrity":            benign("checks a report file's signature"),
	"bedrock-agentcore:RetrieveMemoryRecords":                   benign("reads memory records; a data read, writes nothing"),
	"bedrock-mantle:CallWithBearerToken":                        sideEffect(ruleCreds, "authorises calls made with a Bedrock API key, a credential outside the role session", "bedrock-mantle:CallWithBearerToken"),
	"bedrock:AgenticRetrieveStream":                             benign("retrieves from a knowledge base and streams the results"),
	"bedrock:CheckIngestedDocumentAcl":                          benign("checks a document's access list"),
	"bedrock:CountTokens":                                       benign("counts tokens of a supplied prompt"),
	"chime:Retrieve*":                                           sideEffect(ruleCreds, "returns download links to users' attachment exports", "chime:RetrieveDataExports"),
	"chime:Validate*":                                           benign("checks an account resource or an E911 address"),
	"cleanrooms:PreviewPrivacyImpact":                           benign("previews a privacy budget without changing it"),
	"clouddirectory:BatchRead":                                  benign("batched directory reads"),
	"cloudformation:Detect*":                                    sideEffect(ruleChanges, "starts drift detection, which records drift status on stacks (a stack set operation for stack sets)", "cloudformation:DetectStackDrift", "cloudformation:DetectStackResourceDrift", "cloudformation:DetectStackSetDrift"),
	"cloudformation:Estimate*":                                  benign("EstimateTemplateCost returns a calculator link for a template"),
	"cloudformation:ValidateTemplate":                           benign("checks a template's syntax"),
	"cloudwatch:GenerateQuery":                                  benign("AWS-internal AI helper that drafts a query string"),
	"cloudwatch:GenerateQueryResultsSummary":                    benign("AWS-internal AI helper that summarises query results"),
	"codeartifact:ReadFromRepository":                           benign("reads packages; a data read, writes nothing"),
	"codecommit:GitPull":                                        benign("git fetch/clone; a data read, writes nothing"),
	"codestar:Verify*":                                          benign("VerifyServiceRole checks that the service role exists (service retired, absent from the reference)"),
	"cognito-idp:AdminGet*":                                     benign("reads a user, device or auth factors"),
	"cognito-idp:AdminList*":                                    benign("lists a user's devices, groups or auth events"),
	"comprehend:BatchDetect*":                                   benign("real-time text analysis; returns results, stores nothing"),
	"comprehend:Classify*":                                      benign("ClassifyDocument runs real-time inference on an endpoint; stores nothing"),
	"comprehend:Contains*":                                      benign("ContainsPiiEntities; returns results, stores nothing"),
	"comprehend:Detect*":                                        benign("real-time text analysis; returns results, stores nothing"),
	"config:Deliver*":                                           sideEffect(ruleChanges, "writes a configuration snapshot to the delivery channel's S3 bucket", "config:DeliverConfigSnapshot"),
	"datapipeline:EvaluateExpression":                           benign("evaluates an expression against a pipeline object"),
	"datapipeline:Validate*":                                    benign("ValidatePipelineDefinition checks a definition without saving it"),
	"devops-guru:StartCostEstimation":                           benign("starts a report job (a cost estimate), like servicequotas:StartQuotaUtilizationReport"),
	"dms:Test*":                                                 sideEffect(ruleSends, "TestConnection connects a replication instance to an endpoint that may be outside AWS, using its stored credentials, and records the result", "dms:TestConnection"),
	"ds:Check*":                                                 benign("CheckAlias checks whether a directory alias is free"),
	"ds:Verify*":                                                sideEffect(ruleSends, "VerifyTrust contacts the trusted, possibly on-premises, domain and updates the trust's status", "ds:VerifyTrust"),
	"dynamodb:PartiQLSelect":                                    benign("PartiQL SELECT; a data read, writes nothing"),
	"dynamodb:ReadDataForReplication":                           benign("reads table data for replication; writes nothing"),
	"ecr-public:BatchCheckLayerAvailability":                    benign("checks whether image layers exist"),
	"ecr:BatchCheck*":                                           benign("BatchCheckLayerAvailability checks whether image layers exist"),
	"eks:AccessKubernetesApi":                                   sideEffect(ruleCreds, "opens the cluster's Kubernetes API, whose limits are Kubernetes RBAC, not IAM", "eks:AccessKubernetesApi"),
	"elasticbeanstalk:Check*":                                   benign("CheckDNSAvailability checks whether a CNAME is free"),
	"elasticbeanstalk:Request*":                                 sideEffect(ruleChanges, "RequestEnvironmentInfo has instances bundle their logs and store them in S3", "elasticbeanstalk:RequestEnvironmentInfo"),
	"elasticbeanstalk:Retrieve*":                                sideEffect(ruleCreds, "RetrieveEnvironmentInfo returns presigned S3 URLs to the bundled logs", "elasticbeanstalk:RetrieveEnvironmentInfo"),
	"elasticbeanstalk:Validate*":                                benign("ValidateConfigurationSettings checks settings without applying them"),
	"elastictranscoder:Read*":                                   benign("ReadJob, ReadPipeline, ReadPreset are reads"),
	"elemental-inference:ExportDictionaryEntries":               sideEffect(ruleUnknown, "an export in a new service whose destination is undocumented", "elemental-inference:ExportDictionaryEntries"),
	"es:ESHttpGet":                                              benign("HTTP GET to a search domain; a data read"),
	"es:ESHttpHead":                                             benign("HTTP HEAD to a search domain; a data read"),
	"events:Test*":                                              benign("TestEventPattern matches a pattern against a supplied event"),
	"evidently:TestSegmentPattern":                              benign("matches a pattern against a supplied payload"),
	"gamelift:ResolveAlias":                                     benign("returns the fleet an alias points to"),
	"glue:CheckSchemaVersionValidity":                           benign("checks a schema's syntax"),
	"healthlake:ReadResource":                                   benign("reads a FHIR resource; a data read, writes nothing"),
	"iam:Generate*":                                             benign("starts a report job (credential report, access reports); the reports hold no secrets"),
	"iam:Simulate*":                                             benign("policy simulation; evaluates, changes nothing"),
	"identitystore:IsMemberInGroups":                            benign("checks group membership"),
	"inspector:Preview*":                                        benign("PreviewAgents lists agents an assessment target would cover"),
	"iotanalytics:SampleChannelData":                            benign("returns sample messages from a channel; a data read"),
	"kinesisanalytics:Discover*":                                benign("DiscoverInputSchema infers a schema from sample records"),
	"lightsail:Is*":                                             benign("IsVpcPeered checks VPC peering"),
	"lightsail:IsVpcPeered":                                     benign("checks VPC peering"),
	"logs:FilterLogEvents":                                      benign("reads log events; a data read"),
	"logs:StartLiveTail":                                        benign("starts a live tail of log events; a data read, writes nothing"),
	"logs:StartQuery":                                           benign("starts a Logs Insights query; a data read"),
	"logs:StopLiveTail":                                         benign("cancels a running live tail"),
	"logs:StopQuery":                                            benign("cancels a running query"),
	"logs:TestMetricFilter":                                     benign("matches a filter against supplied log lines"),
	"mediaconnect:DiscoverGatewayPollEndpoint":                  benign("returns the endpoint a gateway instance polls"),
	"mediaconvert:Probe":                                        sideEffect(ruleSends, "fetches a caller-chosen input URL, which may be outside AWS", "mediaconvert:Probe"),
	"observabilityadmin:TestTelemetryPipeline":                  benign("runs a pipeline configuration over supplied sample records"),
	"observabilityadmin:ValidateTelemetryPipelineConfiguration": benign("checks a pipeline configuration"),
	"polly:SynthesizeSpeech":                                    benign("returns synthesised audio in the response; stores nothing"),
	"rds:Download*":                                             benign("DownloadDBLogFilePortion, DownloadCompleteDBLogFile return log text to the caller; a data read"),
	"rekognition:CompareFaces":                                  benign("image analysis; returns results, stores nothing"),
	"rekognition:Detect*":                                       benign("image analysis; returns results, stores nothing"),
	"rekognition:RecognizeCelebrities":                          benign("image analysis; returns results, stores nothing"),
	"route53:Test*":                                             benign("TestDNSAnswer returns the answer a record would give"),
	"route53domains:Check*":                                     benign("checks whether a domain is available or transferable"),
	"s3express:CreateSession":                                   sideEffect(ruleCreds, "returns session credentials for directory buckets, which may be read-write", "s3express:CreateSession"),
	"servicediscovery:DiscoverInstances":                        benign("returns registered instances"),
	"servicediscovery:DiscoverInstancesRevision":                benign("returns the instances' revision number"),
	"servicequotas:StartQuotaUtilizationReport":                 benign("starts a report job"),
	"sns:Check*":                                                benign("CheckIfPhoneNumberIsOptedOut"),
	"sqs:Receive*":                                              sideEffect(ruleChanges, "ReceiveMessage hides messages from production consumers for the visibility timeout and counts toward redrive", "sqs:ReceiveMessage"),
	"states:ValidateStateMachineDefinition":                     benign("checks a definition without saving it"),
	"swf:Count*":                                                benign("counts executions or pending tasks"),
	"transfer:TestIdentityProvider":                             sideEffect(ruleSends, "calls the server's custom identity provider, which may front a non-AWS system, with a supplied user name and password", "transfer:TestIdentityProvider"),
	"verifiedpermissions:IsAuthorized":                          benign("evaluates an authorisation request"),
	"verifiedpermissions:IsAuthorizedWithToken":                 benign("evaluates an authorisation request; the token is an input, none is returned"),
	"wafv2:CheckCapacity":                                       benign("computes the capacity units of supplied rules"),
	"wellarchitected:ExportLens":                                benign("returns a custom lens's JSON in the response; writes nothing"),
	"workdocs:CheckAlias":                                       benign("checks whether a site alias is free"),
	"xray:CancelTraceRetrieval":                                 benign("cancels a running trace retrieval"),
	"xray:StartTraceRetrieval":                                  benign("starts an asynchronous trace read, like logs:StartQuery"),
})

func lowerKeys(m map[string]VerbEntry) map[string]VerbEntry {
	out := make(map[string]VerbEntry, len(m))
	for k, v := range m {
		lk := strings.ToLower(k)
		if _, dup := out[lk]; dup {
			panic("awsprobe: duplicate NonStandard key " + k)
		}
		out[lk] = v
	}
	return out
}
