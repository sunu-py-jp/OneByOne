export namespace model {

	export class ChangeLineRange {
	    beforeStart: number;
	    beforeEnd: number;
	    afterStart: number;
	    afterEnd: number;

	    static createFrom(source: any = {}) {
	        return new ChangeLineRange(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.beforeStart = source["beforeStart"];
	        this.beforeEnd = source["beforeEnd"];
	        this.afterStart = source["afterStart"];
	        this.afterEnd = source["afterEnd"];
	    }
	}
	export class ChangeReportItem {
	    id: string;
	    ruleId: string;
	    ruleTitle: string;
	    location: string;
	    risk: string;
	    change: string;
	    expected: string;
	    status: string;
	    reason: string;
	    lineRanges?: ChangeLineRange[];
	    attributionVersion?: number;
	    sourceAttemptId?: string;

	    static createFrom(source: any = {}) {
	        return new ChangeReportItem(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.ruleId = source["ruleId"];
	        this.ruleTitle = source["ruleTitle"];
	        this.location = source["location"];
	        this.risk = source["risk"];
	        this.change = source["change"];
	        this.expected = source["expected"];
	        this.status = source["status"];
	        this.reason = source["reason"];
	        this.lineRanges = this.convertValues(source["lineRanges"], ChangeLineRange);
	        this.attributionVersion = source["attributionVersion"];
	        this.sourceAttemptId = source["sourceAttemptId"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ReviewHoldAssessment {
	    itemId: string;
	    status: string;
	    reason: string;

	    static createFrom(source: any = {}) {
	        return new ReviewHoldAssessment(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.itemId = source["itemId"];
	        this.status = source["status"];
	        this.reason = source["reason"];
	    }
	}
	export class ReviewIssue {
	    kind?: string;
	    ruleId: string;
	    location: string;
	    lineBasis: string;
	    excerpt: string;
	    startLine: number;
	    endLine: number;
	    reason: string;
	    requestedChange: string;

	    static createFrom(source: any = {}) {
	        return new ReviewIssue(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.ruleId = source["ruleId"];
	        this.location = source["location"];
	        this.lineBasis = source["lineBasis"];
	        this.excerpt = source["excerpt"];
	        this.startLine = source["startLine"];
	        this.endLine = source["endLine"];
	        this.reason = source["reason"];
	        this.requestedChange = source["requestedChange"];
	    }
	}
	export class ReviewAssessment {
	    ruleId: string;
	    status: string;
	    reason: string;

	    static createFrom(source: any = {}) {
	        return new ReviewAssessment(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ruleId = source["ruleId"];
	        this.status = source["status"];
	        this.reason = source["reason"];
	    }
	}
	export class IndependentReview {
	    id: string;
	    candidateId: string;
	    baseHash: string;
	    candidateHash: string;
	    planRevision: number;
	    verdict: string;
	    summary: string;
	    assessments: ReviewAssessment[];
	    issues: ReviewIssue[];
	    holdAssessments?: ReviewHoldAssessment[];
	    usage: Usage;
	    startedAt: string;
	    finishedAt: string;

	    static createFrom(source: any = {}) {
	        return new IndependentReview(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.candidateId = source["candidateId"];
	        this.baseHash = source["baseHash"];
	        this.candidateHash = source["candidateHash"];
	        this.planRevision = source["planRevision"];
	        this.verdict = source["verdict"];
	        this.summary = source["summary"];
	        this.assessments = this.convertValues(source["assessments"], ReviewAssessment);
	        this.issues = this.convertValues(source["issues"], ReviewIssue);
	        this.holdAssessments = this.convertValues(source["holdAssessments"], ReviewHoldAssessment);
	        this.usage = this.convertValues(source["usage"], Usage);
	        this.startedAt = source["startedAt"];
	        this.finishedAt = source["finishedAt"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Usage {
	    uncertain: boolean;
	    inputTokens: number;
	    cachedTokens: number;
	    outputTokens: number;
	    costUsd: number;
	    turns: number;

	    static createFrom(source: any = {}) {
	        return new Usage(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.uncertain = source["uncertain"];
	        this.inputTokens = source["inputTokens"];
	        this.cachedTokens = source["cachedTokens"];
	        this.outputTokens = source["outputTokens"];
	        this.costUsd = source["costUsd"];
	        this.turns = source["turns"];
	    }
	}
	export class Check {
	    name: string;
	    status: string;
	    detail: string;
	    durationMs: number;

	    static createFrom(source: any = {}) {
	        return new Check(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.status = source["status"];
	        this.detail = source["detail"];
	        this.durationMs = source["durationMs"];
	    }
	}
	export class Attempt {
	    id: string;
	    executionId?: string;
	    number: number;
	    startedAt: string;
	    finishedAt: string;
	    outcome: string;
	    partial?: boolean;
	    note: string;
	    rulesApplied: string[];
	    checks: Check[];
	    usage: Usage;
	    diffPath: string;
	    commit: string;
	    baseCommit: string;
	    commitBase?: string;
	    inputHash: string;
	    outputHash: string;
	    repairPath?: string;
	    reviews?: IndependentReview[];
	    changes?: ChangeReportItem[];

	    static createFrom(source: any = {}) {
	        return new Attempt(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.executionId = source["executionId"];
	        this.number = source["number"];
	        this.startedAt = source["startedAt"];
	        this.finishedAt = source["finishedAt"];
	        this.outcome = source["outcome"];
	        this.partial = source["partial"];
	        this.note = source["note"];
	        this.rulesApplied = source["rulesApplied"];
	        this.checks = this.convertValues(source["checks"], Check);
	        this.usage = this.convertValues(source["usage"], Usage);
	        this.diffPath = source["diffPath"];
	        this.commit = source["commit"];
	        this.baseCommit = source["baseCommit"];
	        this.commitBase = source["commitBase"];
	        this.inputHash = source["inputHash"];
	        this.outputHash = source["outputHash"];
	        this.repairPath = source["repairPath"];
	        this.reviews = this.convertValues(source["reviews"], IndependentReview);
	        this.changes = this.convertValues(source["changes"], ChangeReportItem);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}



	export class Config {
	    llmConnectionId?: string;
	    provider?: string;
	    root: string;
	    rulesPath: string;
	    excludedRuleIds?: string[];
	    queuePath: string;
	    rgPath: string;
	    endpoint?: string;
	    deployment?: string;
	    authMode?: string;
	    credential?: string;
	    credentialSet?: boolean;
	    concurrency?: number;
	    inputPricePerMillion: number;
	    cachedInputPricePerMillion: number;
	    outputPricePerMillion: number;

	    static createFrom(source: any = {}) {
	        return new Config(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.llmConnectionId = source["llmConnectionId"];
	        this.provider = source["provider"];
	        this.root = source["root"];
	        this.rulesPath = source["rulesPath"];
	        this.excludedRuleIds = source["excludedRuleIds"];
	        this.queuePath = source["queuePath"];
	        this.rgPath = source["rgPath"];
	        this.endpoint = source["endpoint"];
	        this.deployment = source["deployment"];
	        this.authMode = source["authMode"];
	        this.credential = source["credential"];
	        this.credentialSet = source["credentialSet"];
	        this.concurrency = source["concurrency"];
	        this.inputPricePerMillion = source["inputPricePerMillion"];
	        this.cachedInputPricePerMillion = source["cachedInputPricePerMillion"];
	        this.outputPricePerMillion = source["outputPricePerMillion"];
	    }
	}
	export class DiscardChange {
	    id: string;
	    startedAt: string;
	    finishedAt?: string;
	    state: string;
	    baseCommit: string;
	    restoreCommit: string;
	    commit?: string;
	    inputHash: string;
	    outputHash: string;
	    throughAttempt: number;

	    static createFrom(source: any = {}) {
	        return new DiscardChange(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.startedAt = source["startedAt"];
	        this.finishedAt = source["finishedAt"];
	        this.state = source["state"];
	        this.baseCommit = source["baseCommit"];
	        this.restoreCommit = source["restoreCommit"];
	        this.commit = source["commit"];
	        this.inputHash = source["inputHash"];
	        this.outputHash = source["outputHash"];
	        this.throughAttempt = source["throughAttempt"];
	    }
	}
	export class ExecutionRun {
	    id: string;
	    startedAt: string;
	    finishedAt?: string;
	    status: string;
	    targetCount: number;
	    concurrency?: number;
	    error?: string;

	    static createFrom(source: any = {}) {
	        return new ExecutionRun(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.startedAt = source["startedAt"];
	        this.finishedAt = source["finishedAt"];
	        this.status = source["status"];
	        this.targetCount = source["targetCount"];
	        this.concurrency = source["concurrency"];
	        this.error = source["error"];
	    }
	}
	export class LogEntry {
	    time: string;
	    level: string;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new LogEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.time = source["time"];
	        this.level = source["level"];
	        this.message = source["message"];
	    }
	}
	export class Rule {
	    id: string;
	    title: string;
	    summary: string;
	    pathPattern: string;
	    contentPattern: string;
	    body: string;
	    always: boolean;
	    candidateCount: number;
	    appliedCount: number;

	    static createFrom(source: any = {}) {
	        return new Rule(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.summary = source["summary"];
	        this.pathPattern = source["pathPattern"];
	        this.contentPattern = source["contentPattern"];
	        this.body = source["body"];
	        this.always = source["always"];
	        this.candidateCount = source["candidateCount"];
	        this.appliedCount = source["appliedCount"];
	    }
	}
	export class Task {
	    canDiscardChanges: boolean;
	    discards?: DiscardChange[];
	    file: string;
	    excluded?: boolean;
	    excludedBeforeScope?: boolean;
	    rules: string[];
	    status: string;
	    attempts: number;
	    rulesApplied: string[];
	    note: string;
	    inputHash: string;
	    updatedAt: string;
	    history: Attempt[];
	    resumeRequested?: boolean;

	    static createFrom(source: any = {}) {
	        return new Task(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.canDiscardChanges = source["canDiscardChanges"];
	        this.discards = this.convertValues(source["discards"], DiscardChange);
	        this.file = source["file"];
	        this.excluded = source["excluded"];
	        this.excludedBeforeScope = source["excludedBeforeScope"];
	        this.rules = source["rules"];
	        this.status = source["status"];
	        this.attempts = source["attempts"];
	        this.rulesApplied = source["rulesApplied"];
	        this.note = source["note"];
	        this.inputHash = source["inputHash"];
	        this.updatedAt = source["updatedAt"];
	        this.history = this.convertValues(source["history"], Attempt);
	        this.resumeRequested = source["resumeRequested"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkspaceIssue {
	    id: string;
	    message: string;
	    page: string;
	    ruleId?: string;
	    file?: string;
	    section?: string;

	    static createFrom(source: any = {}) {
	        return new WorkspaceIssue(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.message = source["message"];
	        this.page = source["page"];
	        this.ruleId = source["ruleId"];
	        this.file = source["file"];
	        this.section = source["section"];
	    }
	}
	export class Workspace {
	    id: string;
	    name: string;
	    root: string;
	    issues?: WorkspaceIssue[];

	    static createFrom(source: any = {}) {
	        return new Workspace(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.root = source["root"];
	        this.issues = this.convertValues(source["issues"], WorkspaceIssue);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WorkspaceLock {
	    owner: string;
	    host: string;
	    openedAt: string;

	    static createFrom(source: any = {}) {
	        return new WorkspaceLock(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.owner = source["owner"];
	        this.host = source["host"];
	        this.openedAt = source["openedAt"];
	    }
	}
	export class LLMConnection {
	    id: string;
	    name: string;
	    provider: string;
	    endpoint: string;
	    deployment: string;
	    authMode: string;
	    credential?: string;
	    credentialSet: boolean;
	    oauthTenantId?: string;
	    oauthClientId?: string;
	    oauthUsername?: string;
	    oauthSignedIn: boolean;

	    static createFrom(source: any = {}) {
	        return new LLMConnection(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.provider = source["provider"];
	        this.endpoint = source["endpoint"];
	        this.deployment = source["deployment"];
	        this.authMode = source["authMode"];
	        this.credential = source["credential"];
	        this.credentialSet = source["credentialSet"];
	        this.oauthTenantId = source["oauthTenantId"];
	        this.oauthClientId = source["oauthClientId"];
	        this.oauthUsername = source["oauthUsername"];
	        this.oauthSignedIn = source["oauthSignedIn"];
	    }
	}
	export class State {
	    executionRuns: ExecutionRun[];
	    llmConnections?: LLMConnection[];
	    selectedLLMConnectionId?: string;
	    readOnly: boolean;
	    workspaceLock?: WorkspaceLock;
	    llmSettingsPath: string;
	    workspaces: Workspace[];
	    activeWorkspaceId: string;
	    config: Config;
	    tasks: Task[];
	    rules: Rule[];
	    logs: LogEntry[];
	    running: boolean;
	    phase: string;
	    currentFile: string;
	    currentFiles: string[];
	    filePhases: Record<string, string>;
	    worktree: string;
	    branch: string;
	    scannedCount: number;
	    excludedCount: number;
	    usage: Usage;
	    lastError: string;

	    static createFrom(source: any = {}) {
	        return new State(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.executionRuns = this.convertValues(source["executionRuns"], ExecutionRun);
	        this.llmConnections = this.convertValues(source["llmConnections"], LLMConnection);
	        this.selectedLLMConnectionId = source["selectedLLMConnectionId"];
	        this.readOnly = source["readOnly"];
	        this.workspaceLock = this.convertValues(source["workspaceLock"], WorkspaceLock);
	        this.llmSettingsPath = source["llmSettingsPath"];
	        this.workspaces = this.convertValues(source["workspaces"], Workspace);
	        this.activeWorkspaceId = source["activeWorkspaceId"];
	        this.config = this.convertValues(source["config"], Config);
	        this.tasks = this.convertValues(source["tasks"], Task);
	        this.rules = this.convertValues(source["rules"], Rule);
	        this.logs = this.convertValues(source["logs"], LogEntry);
	        this.running = source["running"];
	        this.phase = source["phase"];
	        this.currentFile = source["currentFile"];
	        this.currentFiles = source["currentFiles"];
	        this.filePhases = source["filePhases"];
	        this.worktree = source["worktree"];
	        this.branch = source["branch"];
	        this.scannedCount = source["scannedCount"];
	        this.excludedCount = source["excludedCount"];
	        this.usage = this.convertValues(source["usage"], Usage);
	        this.lastError = source["lastError"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ExecutionRunResult {
	    run: ExecutionRun;
	    state: State;
	    targetFiles: string[];

	    static createFrom(source: any = {}) {
	        return new ExecutionRunResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.run = this.convertValues(source["run"], ExecutionRun);
	        this.state = this.convertValues(source["state"], State);
	        this.targetFiles = source["targetFiles"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class FileDetail {
	    task: Task;
	    before: string;
	    after: string;
	    diff: string;
	    cumulative: boolean;
	    changes: ChangeReportItem[];

	    static createFrom(source: any = {}) {
	        return new FileDetail(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.task = this.convertValues(source["task"], Task);
	        this.before = source["before"];
	        this.after = source["after"];
	        this.diff = source["diff"];
	        this.cumulative = source["cumulative"];
	        this.changes = this.convertValues(source["changes"], ChangeReportItem);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class GitInstallation {
	    available: boolean;
	    status: string;
	    platform: string;
	    path: string;
	    version: string;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new GitInstallation(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.status = source["status"];
	        this.platform = source["platform"];
	        this.path = source["path"];
	        this.version = source["version"];
	        this.message = source["message"];
	    }
	}



	export class PublishResultsRequest {
	    workspaceId: string;
	    revision: string;
	    branch: string;
	    title: string;
	    message: string;
	    messageAsFile: boolean;

	    static createFrom(source: any = {}) {
	        return new PublishResultsRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.revision = source["revision"];
	        this.branch = source["branch"];
	        this.title = source["title"];
	        this.message = source["message"];
	        this.messageAsFile = source["messageAsFile"];
	    }
	}
	export class ResultPublication {
	    branch: string;
	    commit: string;
	    baseCommit: string;
	    sourceCommit: string;
	    createdAt: string;
	    fileCount: number;
	    title: string;
	    message: string;
	    reportPath?: string;
	    reportBlob?: string;

	    static createFrom(source: any = {}) {
	        return new ResultPublication(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.branch = source["branch"];
	        this.commit = source["commit"];
	        this.baseCommit = source["baseCommit"];
	        this.sourceCommit = source["sourceCommit"];
	        this.createdAt = source["createdAt"];
	        this.fileCount = source["fileCount"];
	        this.title = source["title"];
	        this.message = source["message"];
	        this.reportPath = source["reportPath"];
	        this.reportBlob = source["reportBlob"];
	    }
	}
	export class ResultPublicationFile {
	    file: string;
	    linkPath: string;
	    rulesApplied: string[];
	    summary: string;
	    diff: string;

	    static createFrom(source: any = {}) {
	        return new ResultPublicationFile(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.file = source["file"];
	        this.linkPath = source["linkPath"];
	        this.rulesApplied = source["rulesApplied"];
	        this.summary = source["summary"];
	        this.diff = source["diff"];
	    }
	}
	export class ResultPublicationPreview {
	    workspaceId: string;
	    revision: string;
	    baseCommit: string;
	    sourceCommit: string;
	    suggestedBranch: string;
	    message: string;
	    messageFileThreshold: number;
	    files: ResultPublicationFile[];
	    reportFiles: ResultPublicationFile[];
	    publications: ResultPublication[];

	    static createFrom(source: any = {}) {
	        return new ResultPublicationPreview(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.revision = source["revision"];
	        this.baseCommit = source["baseCommit"];
	        this.sourceCommit = source["sourceCommit"];
	        this.suggestedBranch = source["suggestedBranch"];
	        this.message = source["message"];
	        this.messageFileThreshold = source["messageFileThreshold"];
	        this.files = this.convertValues(source["files"], ResultPublicationFile);
	        this.reportFiles = this.convertValues(source["reportFiles"], ResultPublicationFile);
	        this.publications = this.convertValues(source["publications"], ResultPublication);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}




	export class RuleEdit {
	    expectedRevision?: string;
	    id: string;
	    name: string;
	    description: string;
	    pathPattern: string;
	    contentPattern: string;
	    body: string;

	    static createFrom(source: any = {}) {
	        return new RuleEdit(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.expectedRevision = source["expectedRevision"];
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.pathPattern = source["pathPattern"];
	        this.contentPattern = source["contentPattern"];
	        this.body = source["body"];
	    }
	}
	export class RuleEditor {
	    revision: string;
	    rule: Rule;
	    readOnly: boolean;
	    lockOwner?: string;
	    lockHost?: string;

	    static createFrom(source: any = {}) {
	        return new RuleEditor(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.revision = source["revision"];
	        this.rule = this.convertValues(source["rule"], Rule);
	        this.readOnly = source["readOnly"];
	        this.lockOwner = source["lockOwner"];
	        this.lockHost = source["lockHost"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

	export class TargetFile {
	    file: string;
	    size: number;

	    static createFrom(source: any = {}) {
	        return new TargetFile(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.file = source["file"];
	        this.size = source["size"];
	    }
	}
	export class TargetFileContent {
	    workspaceId: string;
	    root: string;
	    file: string;
	    size: number;
	    content: string;
	    unavailableReason: string;

	    static createFrom(source: any = {}) {
	        return new TargetFileContent(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.root = source["root"];
	        this.file = source["file"];
	        this.size = source["size"];
	        this.content = source["content"];
	        this.unavailableReason = source["unavailableReason"];
	    }
	}
	export class TargetFileList {
	    workspaceId: string;
	    root: string;
	    files: TargetFile[];
	    truncated: boolean;
	    limit: number;

	    static createFrom(source: any = {}) {
	        return new TargetFileList(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.workspaceId = source["workspaceId"];
	        this.root = source["root"];
	        this.files = this.convertValues(source["files"], TargetFile);
	        this.truncated = source["truncated"];
	        this.limit = source["limit"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}





}

