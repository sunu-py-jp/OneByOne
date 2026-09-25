export interface Command {
  name: string;
  executable: string;
  args: string[];
}
export interface TargetFileList {
  workspaceId: string;
  root: string;
  files: { file: string; size: number }[];
  truncated: boolean;
  limit: number;
}
export interface TargetFileContent {
  workspaceId: string;
  root: string;
  file: string;
  size: number;
  content: string;
  unavailableReason: string;
}
export interface Config {
  llmConnectionId?: string;
  provider: "openai" | "azure" | "claude";
  root: string;
  rulesPath: string;
  rulePackageName: string;
  legacyPath: string;
  queuePath: string;
  rgPath: string;
  endpoint: string;
  deployment: string;
  authMode: string;
  credential: string;
  credentialSet: boolean;
  includeGlobs: string[];
  excludeGlobs: string[];
  checkCommands: Command[];
  maxAttempts: number;
  maxTurns: number;
  maxOutputTokens: number;
  maxFileBytes: number;
  timeoutSeconds: number;
  maxCostUSD: number;
  inputPricePerMillion: number;
  cachedInputPricePerMillion: number;
  outputPricePerMillion: number;
}
export interface RuleContent {
  overview: string;
  before: string;
  after: string;
  notes: string;
  holdConditions: string;
  pattern: string;
}
export interface Rule extends RuleContent {
  id: string;
  title: string;
  summary: string;
  always: boolean;
  candidateCount: number;
  appliedCount: number;
}
export interface RuleEdit extends RuleContent {
  id: string;
  name: string;
  expectedRevision?: string;
}
export interface RuleEditor {
  rule: Rule;
  revision: string;
  readOnly: boolean;
  lockOwner?: string;
  lockHost?: string;
}
export interface Check {
  name: string;
  status: string;
  detail: string;
  durationMs: number;
}
export interface Usage {
  inputTokens: number;
  cachedTokens: number;
  outputTokens: number;
  costUsd: number;
  turns: number;
  uncertain?: boolean;
}
export interface IndependentReview {
  id: string;
  candidateId: string;
  baseHash: string;
  candidateHash: string;
  planRevision: number;
  verdict: "passed" | "needs_changes" | "needs_human" | "running" | "error";
  summary: string;
  assessments: { ruleId: string; status: string; reason: string }[];
  issues: { ruleId: string; location: string; lineBasis: "before" | "after"; excerpt: string; reason: string; requestedChange: string }[];
  usage: Usage;
  startedAt: string;
  finishedAt: string;
}
export interface ChangeLineRange {
  beforeStart: number;
  beforeEnd: number;
  afterStart: number;
  afterEnd: number;
}
export interface ChangeReportItem {
  sourceAttemptId?: string;
  origin?: "latest" | "previous" | "mixed";
  lineRanges?: ChangeLineRange[];
  id: string;
  ruleId: string;
  ruleTitle: string;
  location: string;
  risk: string;
  change: string;
  expected: string;
  status: "fixed" | "needs_human" | "not_applied" | "pending" | "unchanged";
  reason: string;
}
export interface Attempt {
  id: string;
  executionId?: string;
  number: number;
  startedAt: string;
  finishedAt: string;
  outcome: string;
  note: string;
  rulesApplied: string[];
  checks: Check[];
  usage: Usage;
  diffPath: string;
  commit: string;
  repairPath?: string;
  reviews?: IndependentReview[];
  changes?: ChangeReportItem[];
}
export interface DiscardChange {
  id: string;
  startedAt: string;
  finishedAt: string;
  baseCommit: string;
  restoreCommit: string;
  commit: string;
  inputHash: string;
  outputHash: string;
  throughAttempt: number;
  state: "prepared" | "done";
}
export interface Task {
  file: string;
  excluded?: boolean;
  rules: string[];
  status: string;
  attempts: number;
  resumeRequested?: boolean;
  canDiscardChanges?: boolean;
  discards?: DiscardChange[];
  rulesApplied: string[];
  note: string;
  inputHash: string;
  updatedAt: string;
  history: Attempt[];
}
export interface LogEntry {
  time: string;
  level: string;
  message: string;
}
export interface WorkspaceIssue {
  id: string;
  message: string;
  page: "target" | "rules" | "review" | "run" | "results";
  ruleId?: string;
  file?: string;
  section?: string;
}
export interface Workspace {
  id: string;
  name: string;
  root: string;
  issues?: WorkspaceIssue[];
}
export interface LLMConnection {
  id: string;
  name: string;
  provider: Config["provider"];
  endpoint: string;
  deployment: string;
  authMode: string;
  credential: string;
  credentialSet: boolean;
  oauthTenantId?: string;
  oauthClientId?: string;
  oauthUsername?: string;
  oauthSignedIn?: boolean;
}
export interface ExecutionRun {
  id: string;
  startedAt: string;
  finishedAt: string;
  status: "running" | "completed" | "stopped" | "failed";
  targetCount: number;
  error: string;
}
export interface ExecutionRunResult {
  run: ExecutionRun;
  state: State;
  targetFiles: string[];
}
export interface ResultPublicationFile {
  file: string;
  rulesApplied: string[];
  summary: string;
  diff?: string;
}
export interface ResultPublication {
  branch: string;
  commit: string;
  baseCommit: string;
  sourceCommit: string;
  createdAt: string;
  fileCount: number;
  title: string;
  message: string;
}
export interface ResultPublicationPreview {
  workspaceId: string;
  revision: string;
  baseCommit: string;
  sourceCommit: string;
  suggestedBranch: string;
  message: string;
  files: ResultPublicationFile[];
  publications: ResultPublication[];
}
export interface PublishResultsRequest {
  workspaceId: string;
  revision: string;
  branch: string;
  title: string;
  message: string;
}
export interface State {
  executionRuns: ExecutionRun[];
  workspaces: Workspace[];
  llmConnections: LLMConnection[];
  selectedLLMConnectionId: string;
  readOnly: boolean;
  workspaceLock?: { owner: string; host: string; openedAt: string };
  llmSettingsPath: string;
  activeWorkspaceId: string;
  config: Config;
  tasks: Task[];
  rules: Rule[];
  logs: LogEntry[];
  running: boolean;
  phase: string;
  currentFile: string;
  worktree: string;
  branch: string;
  scannedCount: number;
  excludedCount: number;
  usage: Usage;
  lastError: string;
}
export interface FileDetail {
  task: Task;
  before: string;
  after: string;
  diff: string;
  cumulative?: boolean;
  changes?: ChangeReportItem[];
}
export interface GitInstallation {
  available: boolean;
  status: "ready" | "missing" | "unusable" | "bundle_error";
  platform: string;
  path: string;
  version: string;
  message: string;
}
export interface Backend {
  CheckGitInstallation(): Promise<GitInstallation>;
  OpenGitInstallGuide(): Promise<void>;
  CreateWorkspace(name: string, root: string): Promise<State>;
  CreateDemoWorkspace(name: string, root: string): Promise<State>;
  ValidateTargetFolder(root: string): Promise<string>;
  CreateDemoProject(parent: string): Promise<string>;
  SetTaskSelection(files: string[]): Promise<State>;
  ChangeTargetFolder(root: string): Promise<State>;
  DuplicateWorkspace(name: string): Promise<State>;
  SelectWorkspace(id: string): Promise<State>;
  RenameWorkspace(name: string): Promise<State>;
  DeleteWorkspace(id: string): Promise<State>;
  DeleteRule(id: string): Promise<State>;
  GetState(): Promise<State>;
  SaveConfig(config: Config): Promise<State>;
  ChooseDirectory(kind: "root" | "demo" | "rules"): Promise<string>;
  ChooseLegacy(): Promise<string>;
  ChooseRulePackage(): Promise<string>;
  ImportRulePackage(path: string, mode: "replace" | "merge"): Promise<State>;
  ExportRulePackage(): Promise<string>;
  Scan(): Promise<State>;
  Start(limit: number): Promise<void>;
  Stop(): Promise<void>;
  RetryTasks(files: string[]): Promise<State>;
  DiscardFileChanges(file: string): Promise<State>;
  GetFileDetail(file: string, attempt: number): Promise<FileDetail>;
  GetExecutionRun(id: string): Promise<ExecutionRunResult>;
  GetExecutionFileDetail(id: string, file: string, attempt: number): Promise<FileDetail>;
  ListTargetFiles(workspaceId: string, root: string): Promise<TargetFileList>;
  ReadTargetFile(workspaceId: string, root: string, file: string): Promise<TargetFileContent>;
  ReadExecutionFile(workspaceId: string, root: string, file: string): Promise<TargetFileContent>;
  ReadRule(id: string): Promise<Rule>;
  OpenRule(id: string): Promise<RuleEditor>;
  CloseRule(): Promise<void>;
  SaveRule(edit: RuleEdit): Promise<State>;
  SaveRules(edits: RuleEdit[]): Promise<State>;
  CreateRule(edit: RuleEdit): Promise<State>;
  ExportReport(): Promise<string>;
  ExportExecutionReport(id: string): Promise<string>;
  OpenWorktree(): Promise<void>;
  GetResultPublicationPreview(): Promise<ResultPublicationPreview>;
  GetResultPublicationFileDiff(workspaceId: string, revision: string, file: string): Promise<string>;
  PublishResults(request: PublishResultsRequest): Promise<ResultPublication>;
  TestConnection(): Promise<string>;
  SaveLLMConnection(connection: LLMConnection): Promise<State>;
  DeleteLLMConnection(id: string): Promise<State>;
  SelectLLMConnection(id: string): Promise<State>;
  ClearLLMConnectionCredential(id: string): Promise<State>;
  TestLLMConnection(id: string): Promise<string>;
  SignInLLMConnection(id: string): Promise<State>;
  CancelLLMSignIn(): Promise<void>;
  SignOutLLMConnection(id: string): Promise<State>;
}
export const emptyLLMConnection: LLMConnection = {
  id: "", name: "", provider: "azure", endpoint: "", deployment: "",
  authMode: "api_key", credential: "", credentialSet: false,
  oauthTenantId: "", oauthClientId: "", oauthUsername: "", oauthSignedIn: false,
};
export const defaultConfig: Config = {
  provider: "azure",
  root: "",
  rulesPath: "",
  rulePackageName: "",
  legacyPath: "",
  queuePath: "",
  rgPath: "",
  endpoint: "",
  deployment: "",
  authMode: "api_key",
  credential: "",
  credentialSet: false,
  includeGlobs: [],
  excludeGlobs: [
    ".git/**",
    "node_modules/**",
    "vendor/**",
    "dist/**",
    "build/**",
  ],
  checkCommands: [],
  maxAttempts: 0,
  maxTurns: 0,
  maxOutputTokens: 0,
  maxFileBytes: 0,
  timeoutSeconds: 0,
  maxCostUSD: 0,
  inputPricePerMillion: 0,
  cachedInputPricePerMillion: 0,
  outputPricePerMillion: 0,
};
export const emptyUsage: Usage = {
  inputTokens: 0,
  cachedTokens: 0,
  outputTokens: 0,
  costUsd: 0,
  turns: 0,
};
export const emptyState: State = {
  executionRuns: [],
  workspaces: [],
  llmConnections: [],
  selectedLLMConnectionId: "",
  readOnly: false,
  llmSettingsPath: "",
  activeWorkspaceId: "",
  config: defaultConfig,
  tasks: [],
  rules: [],
  logs: [],
  running: false,
  phase: "idle",
  currentFile: "",
  worktree: "",
  branch: "",
  scannedCount: 0,
  excludedCount: 0,
  usage: emptyUsage,
  lastError: "",
};
export function normalizeTask(task: Task): Task {
  return {
    ...task,
    rules: task.rules || [],
    rulesApplied: task.rulesApplied || [],
    canDiscardChanges: Boolean(task.canDiscardChanges),
    discards: task.discards || [],
    history: (task.history || []).map((a) => ({
      ...a,
      rulesApplied: a.rulesApplied || [],
      checks: a.checks || [],
      changes: a.changes || [],
      usage: { ...emptyUsage, ...a.usage },
      reviews: (a.reviews || []).map(review => ({
        ...review,
        assessments: review.assessments || [],
        issues: review.issues || [],
        usage: { ...emptyUsage, ...review.usage },
      })),
    })),
  };
}
export function normalizeState(state: State): State {
  return {
    ...emptyState,
    ...state,
    executionRuns: state.executionRuns || [],
    config: {
      ...defaultConfig,
      ...state.config,
      includeGlobs: state.config?.includeGlobs || [],
      excludeGlobs: state.config?.excludeGlobs || [],
      checkCommands: state.config?.checkCommands || [],
    },
    workspaces: state.workspaces || [],
    llmConnections: (state.llmConnections || []).map((connection) => ({
      ...emptyLLMConnection,
      ...connection,
      credential: "",
    })),
    tasks: (state.tasks || []).map(normalizeTask),
    rules: state.rules || [],
    logs: state.logs || [],
    usage: { ...emptyUsage, ...state.usage },
  };
}

export function normalizeExecutionRunResult(result: ExecutionRunResult): ExecutionRunResult {
  return { ...result, state: normalizeState(result.state), targetFiles: result.targetFiles || [] };
}
