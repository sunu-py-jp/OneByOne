import {
  Children,
  cloneElement,
  isValidElement,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { api, isNative, isPreview } from "./bridge";
import { Icon, type IconName } from "./icons";
import { HelpTip } from "./HelpTip";
import { HoverTip } from "./HoverTip";
import { RuleBodyEditor } from "./RuleBodyEditor";
import { nextRuleId, ruleDraftErrors, ruleIdError, isCommonRule } from "./rule-validation";
import { RuleScopeIcon } from "./RuleScopeIcon";
import { RulePackageActions, RulePackageImportDialog } from "./RulePackageActions";
import { WorkspaceIssues } from "./WorkspaceIssues";
import { DeleteConfirmation } from "./DeleteConfirmation";
import { Toast } from "./Toast";
import { TargetFilesPanel } from "./TargetFilesPanel";
import { ExecutionRuleSelection } from "./ExecutionRuleSelection";
import { ConcurrencyControl } from "./ConcurrencyControl";
import { onlyConcurrencyChanged } from "./concurrency";
import { TargetFolderBrowser } from "./TargetFolderBrowser";
import { ExecutionResultsPanel as ResultsPanel } from "./ExecutionResultsPanel";
import { ResultPublicationPanel } from "./ResultPublicationPanel";
import type { PublicationDraft } from "./result-publication";
import { GitSetupDialog } from "./GitSetupDialog";
import { useGitInstallation } from "./useGitInstallation";
import { hasMatchingOAuthSession, isOAuthConnection, oauthConfigurationIssue } from "./llm-connection";
import { selectionContextKey } from "./selection";
import { isCompletedTask } from "./task-state";
import { countReadyTargets, hasReadyLLMConnection, workflowAvailability, workflowBlockReasons, type WorkflowPage } from "./workflow-navigation";
import "./workspace-navigation.css";
import {
  emptyState,
  emptyLLMConnection,
  type LLMConnection,
  type Config,
  type Rule,
  type RuleEdit,
  type RuleEditor,
  type RuleContent,
  type State,
  type WorkspaceIssue,
  type ResultPublication,
} from "./types";

type Page = "llm" | WorkflowPage;
const steps: { id: WorkflowPage; label: string; description: string }[] = [
  {
    id: "target",
    label: "対象フォルダ",
    description: "処理するソースのフォルダを確認します。",
  },
  {
    id: "rules",
    label: "ルール",
    description: "変換ルールと適用するファイルの条件を設定します。",
  },
  {
    id: "run",
    label: "実行設定",
    description: "LLM接続と適用ルールを選び、処理対象ファイルを選択します。",
  },
  {
    id: "review",
    label: "確認",
    description: "確定した対象ファイルと候補ルールを確認して実行します。",
  },
  {
    id: "results",
    label: "結果確認",
    description: "修正差分・検証結果・試行履歴を確認します。",
  },
  {
    id: "publish",
    label: "コミット",
    description: "採用済みの変更を新規ブランチの1コミットにまとめます。",
  },
];
type DetailTab = "diff" | "checks" | "history" | "changes";
type Notice = { type: "success" | "error" | "info"; message: string };
const number = (value: number) =>
  new Intl.NumberFormat("ja-JP").format(value || 0);
const basename = (path: string) =>
  path.split(/[\\/]/).filter(Boolean).at(-1) || "プロジェクト未選択";
const time = (value: string) =>
  value
    ? new Date(value).toLocaleTimeString("ja-JP", {
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
      })
    : "—";
const errorMessage = (error: unknown) =>
  error instanceof Error
    ? error.message
    : typeof error === "string"
      ? error
      : JSON.stringify(error);

function Button({
  children,
  icon,
  className = "",
  loading,
  disabledReason,
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement> & {
  icon?: IconName;
  loading?: boolean;
  disabledReason?: string;
}) {
  return (
    <HoverTip reason={props.disabled ? disabledReason : ""}><button type="button" className={`button ${className}`} {...props}>
      {loading ? (
        <span className="spinner" />
      ) : icon ? (
        <Icon name={icon} size={16} />
      ) : null}
      {children}
    </button></HoverTip>
  );
}
function Field({
  label,
  hint,
  action,
  children,
  className = "",
}: {
  label: string;
  hint?: string;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  const controlId = useId();
  // Keep the help button outside the label, including for wrapped path inputs.
  let labelled = false;
  const connectLabel = (nodes: ReactNode): ReactNode =>
    Children.map(nodes, (child) => {
      if (
        !isValidElement<{ children?: ReactNode; id?: string }>(child) ||
        typeof child.type !== "string" ||
        labelled
      ) return child;
      if (["input", "select", "textarea"].includes(child.type)) {
        labelled = true;
        return cloneElement(child, { id: controlId });
      }
      return child.props.children
        ? cloneElement(child, {}, connectLabel(child.props.children))
        : child;
    });
  return (
    <div className={`field ${className}`}>
      <div className={`field-heading${action ? " field-heading-with-action" : ""}`}>
        <label className="field-label" htmlFor={controlId}>
          {label}
        </label>
        {hint && <HelpTip label={label}>{hint}</HelpTip>}
        {action && <div className="field-heading-action">{action}</div>}
      </div>
      {connectLabel(children)}
    </div>
  );
}
function RuleErrorDetails({ issues }: { issues: WorkspaceIssue[] }) {
  return <section id="rule-error-details" className="rule-error-details" aria-label="エラー詳細" tabIndex={-1}>
    <h3><Icon name="warning" size={16} />エラー詳細</h3>
    <ul>{issues.map((issue) => <li key={issue.id}>{issue.message}</li>)}</ul>
  </section>;
}

export default function App() {
  const [state, setState] = useState<State>(emptyState);
  const [draft, setDraft] = useState<Config>(emptyState.config);
  const [dirty, setDirty] = useState(false);
  const dirtyRef = useRef(false);
  const [page, setPage] = useState<Page>("target");
  const publicationDrafts = useRef<Record<string, PublicationDraft>>({});
  const publicationDraftKey = `${state.activeWorkspaceId}:${state.config.queuePath}`;
  const [editingConnectionId, setEditingConnectionId] = useState("__new");
  const [connectionDrafts, setConnectionDrafts] = useState<Record<string, LLMConnection>>({});
  const [confirmDeleteConnection, setConfirmDeleteConnection] = useState(false);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState<Notice | null>(null);
  const dismissNotice = useCallback(() => setNotice(null), []);
  const lastNotifiedError = useRef("");
  const [selectedFile, setSelectedFile] = useState("");
  const [detailTab, setDetailTab] = useState<DetailTab>("diff");
  const [ruleSearch, setRuleSearch] = useState("");
  const [selectedRule, setSelectedRule] = useState("");
  const [ruleDetail, setRuleDetail] = useState<Rule | null>(null);
  const [ruleError, setRuleError] = useState("");
  const [ruleReloadVersion, setRuleReloadVersion] = useState(0);
  const [ruleEditor, setRuleEditor] = useState<RuleEditor | null>(null);
  const [touchedRuleDrafts, setTouchedRuleDrafts] = useState<Set<string>>(new Set());
  const [ruleDrafts, setRuleDrafts] = useState<Record<string, RuleEdit>>({});
  const [draftSaveError, setDraftSaveError] = useState<{ key: string; message: string } | null>(null);
  const [packageImport, setPackageImport] = useState<{ path: string; workspaceId: string } | null>(null);
  const ruleSessionQueue = useRef<Promise<unknown>>(Promise.resolve());
  const ruleRequestVersion = useRef(0);
  const [logsOpen, setLogsOpen] = useState(false);
  const [showCredential, setShowCredential] = useState(false);
  const [oauthSigningIn, setOAuthSigningIn] = useState(false);
  const [oauthCancelPending, setOAuthCancelPending] = useState(false);
  const oauthCancelRequested = useRef(false);
  const [workspaceDialog, setWorkspaceDialog] = useState<
    "create" | "rename" | "duplicate" | null
  >(null);
  const [workspaceName, setWorkspaceName] = useState("");
  const [workspaceRoot, setWorkspaceRoot] = useState("");
  const [demoProjectRoot, setDemoProjectRoot] = useState("");
  const [fileSelection, setFileSelection] = useState<Set<string> | null>(null);
  const [selectionContext, setSelectionContext] = useState<string | null>(null);
  const [workspaceRootError, setWorkspaceRootError] = useState("");
  const [targetSelection, setTargetSelection] = useState<{ workspaceId: string; path: string; error: string } | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<{ kind: "workspace" | "rule"; id: string; workspaceId: string; name: string } | null>(null);
  const [deleteError, setDeleteError] = useState("");
  const [discardTarget, setDiscardTarget] = useState<{ file: string; workspaceId: string } | null>(null);
  const [discardError, setDiscardError] = useState("");
  const workspaceAddRef = useRef<HTMLButtonElement>(null);
  const workspaceRef = useRef("");
  const pendingIssueRef = useRef<{ workspaceId: string; issue: WorkspaceIssue } | null>(null);
  const [issueDestination, setIssueDestination] = useState<{ workspaceId: string; issue: WorkspaceIssue } | null>(null);
  const mainContentRef = useRef<HTMLElement>(null);
  const ruleSectionRef = useRef<HTMLElement>(null);
  const usable = isNative && !isPreview;
  const gitSetup = useGitInstallation(usable);
  const showGitSetup = gitSetup.open && !workspaceDialog && !packageImport && !deleteTarget && !discardTarget && !state.running && !busy;
  const entryLocked = Boolean(busy) || state.running || (!usable && !isPreview);
  const locked = Boolean(busy) || state.running || !usable || state.readOnly;
  const replaceDraft = useCallback((config: Config) => {
    setDraft({ ...config, credential: "" });
    setDirty(false);
    dirtyRef.current = false;
  }, []);
  const acceptState = useCallback(
    (next: State, forceDraft = false) => {
      setState(next);
      if (forceDraft || !dirtyRef.current) replaceDraft(next.config);
    },
    [replaceDraft],
  );
  const updateDraft = <K extends keyof Config>(key: K, value: Config[K]) => {
    setDraft((prev) => ({ ...prev, [key]: value }));
    setDirty(true);
    dirtyRef.current = true;
  };

  const retryGitInstallation = async () => {
    if (!await gitSetup.retry()) return;
    try {
      acceptState(await api.GetState());
      setNotice({ type: "success", message: "Gitを利用できます。" });
    } catch (error) {
      setNotice({ type: "error", message: errorMessage(error) });
    }
  };

  useEffect(() => {
    let mounted = true;
    api
      .GetState()
      .then((next) => {
        if (mounted) acceptState(next, true);
      })
      .catch((error) => {
        if (mounted) setNotice({ type: "error", message: errorMessage(error) });
      })
      .finally(() => {
        if (mounted) setLoading(false);
      });
    return () => {
      mounted = false;
    };
  }, [acceptState]);
  useEffect(() => {
    if (!state.running || !usable) return;
    let disposed = false;
    let polling = false;
    const interval = window.setInterval(async () => {
      if (polling) return;
      polling = true;
      try {
        const next = await api.GetState();
        if (!disposed) acceptState(next);
      } catch (error) {
        if (!disposed)
          setNotice({ type: "error", message: errorMessage(error) });
      } finally {
        polling = false;
      }
    }, 1500);
    return () => {
      disposed = true;
      clearInterval(interval);
    };
  }, [state.running, usable, acceptState]);
  const selectedRuleSource = state.rules.find(
    (rule) => rule.id === selectedRule,
  );
  const activeWorkspaceIssues = state.workspaces.find((workspace) => workspace.id === state.activeWorkspaceId)?.issues || [];
  const targetSelectionIssue: WorkspaceIssue | null = targetSelection?.error
    ? { id: "target-selection", message: targetSelection.error, page: "target", section: "target" }
    : null;
  const selectedTarget = targetSelection?.workspaceId === state.activeWorkspaceId ? targetSelection : null;
  const targetFolderError = selectedTarget?.error || activeWorkspaceIssues.filter((issue) => issue.page === "target").map((issue) => issue.message).join("\n");
  const globalError = activeWorkspaceIssues.some((issue) => issue.message === state.lastError) ? "" : state.lastError;
  useEffect(() => {
    const key = globalError ? `${state.activeWorkspaceId}\0${globalError}` : "";
    if (lastNotifiedError.current === key) return;
    lastNotifiedError.current = key;
    if (globalError) setNotice((current) => current?.message === globalError ? current : { type: "error", message: globalError });
  }, [state.activeWorkspaceId, globalError]);
  // A new rule's ID must not reuse a saved or unreadable rule's ID.
  const takenRuleIds = new Set([...state.rules.map(rule => rule.id), ...activeWorkspaceIssues.flatMap(issue => issue.ruleId ? [issue.ruleId] : [])].map(id => id.toLowerCase()));
  const workspaceDraftErrors = Object.entries(ruleDrafts)
    .filter(([key]) => key.startsWith(`${state.activeWorkspaceId}/`))
    .flatMap(([key, edit]) => ruleDraftErrors(edit, key.endsWith("/__new") ? takenRuleIds : undefined).map((message, index) => ({ key, issue: { id: `draft:${key}:${index}`, message, page: "rules" as const, ruleId: key.slice(state.activeWorkspaceId.length + 1) } })));
  const firstRuleDraftError = workspaceDraftErrors[0]?.issue.message || "";
  const touchRuleDraft = (key = `${state.activeWorkspaceId}/${selectedRule}`) => setTouchedRuleDrafts(previous => new Set([...previous, key]));
  const revealRuleDraftErrors = () => setTouchedRuleDrafts(previous => new Set([...previous, ...workspaceDraftErrors.map(item => item.key)]));
  const ruleIssuesFor = (id: string) => [
    ...activeWorkspaceIssues.filter(issue => issue.ruleId === id),
    ...workspaceDraftErrors.filter(item => item.issue.ruleId === id && touchedRuleDrafts.has(item.key)).map(item => item.issue),
    ...(draftSaveError?.key === `${state.activeWorkspaceId}/${id}` ? [{ id: "draft-save", message: draftSaveError.message, page: "rules" as const, ruleId: id }] : []),
  ];
  const selectRule = (id: string) => { touchRuleDraft(); setSelectedRule(id); };
  const knownRuleIssues = ruleIssuesFor(selectedRule);
  const selectedRuleIssues: WorkspaceIssue[] = [...knownRuleIssues];
  if (ruleError && selectedRule && knownRuleIssues.length === 0) {
    selectedRuleIssues.push({ id: "rule-read", message: ruleError, page: "rules", ruleId: selectedRule });
  }
  const selectedRuleVersion = JSON.stringify([state.config.rulesPath, selectedRuleSource, activeWorkspaceIssues.filter(issue => issue.ruleId === selectedRule), ruleReloadVersion]);
  const ruleDraftKey = `${state.activeWorkspaceId}/${selectedRule}`;
  const currentRuleDraft = ruleDrafts[ruleDraftKey];
  const ruleValue = (field: keyof RuleContent) => currentRuleDraft?.[field] ?? ruleDetail?.[field] ?? "";
  const hasRuleChanges = Object.keys(ruleDrafts).some((key) => key.startsWith(`${state.activeWorkspaceId}/`));
  const staleRuleDraft = Boolean(currentRuleDraft?.expectedRevision && ruleEditor && currentRuleDraft.expectedRevision !== ruleEditor.revision);
  useEffect(() => {
    const request = ++ruleRequestVersion.current;
    setRuleDetail(null);
    setRuleEditor(null);
    setRuleError("");
    // Close and open operations share one ordered queue, including cleanup. A
    // slow response from the previous selection cannot release the new lease.
    ruleSessionQueue.current = ruleSessionQueue.current.catch(() => {}).then(async () => {
      await api.CloseRule();
      if (request !== ruleRequestVersion.current || page !== "rules" || !selectedRule || selectedRule === "__new") return;
      const opened = await api.OpenRule(selectedRule);
      if (request === ruleRequestVersion.current) {
        setRuleEditor(opened);
        setRuleDetail(opened.rule);
      }
    }).catch((error) => {
      if (request === ruleRequestVersion.current) setRuleError(errorMessage(error));
    });
    return () => {
      ++ruleRequestVersion.current;
      ruleSessionQueue.current = ruleSessionQueue.current.catch(() => {}).then(() => api.CloseRule()).catch(() => {});
    };
  }, [page, selectedRule, selectedRuleVersion, state.activeWorkspaceId]);

  const changeRuleDraft = <K extends keyof RuleEdit>(key: K, value: RuleEdit[K]) => {
    if (draftSaveError?.key === ruleDraftKey) setDraftSaveError(null);
    const initial = currentRuleDraft || {
      id: selectedRule, name: ruleDetail?.title || "", expectedRevision: ruleEditor?.revision,
      description: ruleDetail?.summary || "", pathPattern: ruleDetail?.pathPattern || "",
      contentPattern: ruleDetail?.contentPattern || "", body: ruleDetail?.body || "",
    };
    setRuleDrafts((previous) => ({ ...previous, [ruleDraftKey]: { ...initial, [key]: value } }));
  };
  const discardRuleDraft = () => {
    if (draftSaveError?.key === ruleDraftKey) setDraftSaveError(null);
    setRuleDrafts((previous) => {
      const next = { ...previous };
      delete next[ruleDraftKey];
      return next;
    });
    setRuleError("");
    if (selectedRule === "__new") setSelectedRule(state.rules[0]?.id || "");
  };
  const beginNewRule = () => {
    touchRuleDraft();
    const key = `${state.activeWorkspaceId}/__new`;
    if (!ruleDrafts[key]) {
      setRuleDrafts((previous) => ({ ...previous, [key]: {
        id: nextRuleId(takenRuleIds), name: "", description: "", pathPattern: "", contentPattern: "",
        body: "# 変更概要\n\n# 変換前\n\n# 変換後\n\n# 補足\n\n# 変換を保留にすべきケース\n\n# 修正後に残っていてはいけないパターン\n",
      } }));
    }
    setSelectedRule("__new");
    setPage("rules");
  };
  async function flushWorkspaceChanges(): Promise<State> {
    if (workspaceDraftErrors.length) {
      revealRuleDraftErrors();
      setSelectedRule(workspaceDraftErrors[0].issue.ruleId);
      setPage("rules");
      throw new Error(firstRuleDraftError);
    }
    let next = state;
    if (fileSelection && !state.readOnly) {
      const changed = next.tasks.some((item) => fileSelection.has(item.file) === Boolean(item.excluded));
      if (changed) {
        next = await api.SetTaskSelection([...fileSelection]);
        acceptState(next);
      }
      setFileSelection(null);
    }
    if (dirtyRef.current && !state.readOnly) next = await save();
    const edits = Object.entries(ruleDrafts).filter(([key]) => key.startsWith(`${state.activeWorkspaceId}/`));
    if (edits.length && !isPreview) {
      await ruleSessionQueue.current;
      try {
        next = await api.SaveRules(edits.map(([, edit]) => ({ ...edit, id: edit.id.trim(), name: edit.name.trim(), description: edit.description.trim() })));
      } catch (error) {
        const message = errorMessage(error);
        const affected = edits.find(([, edit]) => edit.id.trim() && message.includes(`rule ${edit.id.trim()}:`)) || edits[0];
        setDraftSaveError({ key: affected[0], message });
        setSelectedRule(affected[0].slice(state.activeWorkspaceId.length + 1));
        setPage("rules");
        throw error;
      }
      const savedKeys = new Set(edits.map(([key]) => key));
      setRuleDrafts((previous) => Object.fromEntries(Object.entries(previous).filter(([key]) => !savedKeys.has(key))));
      setDraftSaveError(null);
      if (selectedRule === "__new") setSelectedRule(edits.find(([key]) => key.endsWith("/__new"))?.[1].id.trim() || "");
      setRuleReloadVersion((version) => version + 1);
      acceptState(next, true);
    }
    return next;
  }
  async function perform(label: string, operation: () => Promise<void>) {
    setBusy(label);
    setNotice(null);
    try {
      await operation();
    } catch (error) {
      setNotice({ type: "error", message: errorMessage(error) });
    } finally {
      setBusy("");
    }
  }
  async function importRulePackage(path: string, workspaceId: string, mode: "replace" | "merge") {
    if (workspaceId !== state.activeWorkspaceId) throw new Error("ワークスペースが変更されています。取り込み直してください。");
    await flushWorkspaceChanges();
    await ruleSessionQueue.current;
    await api.CloseRule();
    const next = await api.ImportRulePackage(path, mode);
    acceptState(next, true);
    setPackageImport(null);
    setSelectedRule(next.rules[0]?.id || "");
    setRuleReloadVersion(value => value + 1);
    setSelectionContext(null);
    setNotice({ type: "success", message: mode === "merge" ? "ルールをマージしました。" : "ルールを読み込みました。" });
  }
  const beginPackageImport = () => perform("ルール選択", async () => {
    const path = await api.ChooseRulePackage();
    if (!path) return;
    const next = await flushWorkspaceChanges();
    if (next.rules.length || invalidRuleIds.length) setPackageImport({ path, workspaceId: next.activeWorkspaceId });
    else await importRulePackage(path, next.activeWorkspaceId, "replace");
  });
  const exportRulePackage = () => perform("ルール書出し", async () => {
    await flushWorkspaceChanges();
    const path = await api.ExportRulePackage();
    if (path) setNotice({ type: "success", message: `ルールを保存しました: ${path}` });
  });
  async function save() {
    if (state.readOnly) throw new Error("このワークスペースは閲覧専用です。再度開いて編集権限を確認してください。");
    const next = await api.SaveConfig(draft);
    acceptState(next, true);
    return next;
  }
  function openRule(id: string) {
    if (state.running || busy) return;
    void perform("ルールを表示", async () => {
      await flushWorkspaceChanges();
      setSelectedRule(id);
      setPage("rules");
    });
  }
  function selectTask(file: string) {
    setSelectedFile(file);
    setDetailTab("diff");
  }
  const filteredRules = state.rules.map((rule) => {
    const edit = ruleDrafts[`${state.activeWorkspaceId}/${rule.id}`];
    return edit ? { ...rule, title: edit.name, summary: edit.description, pathPattern: edit.pathPattern, contentPattern: edit.contentPattern, body: edit.body, always: isCommonRule(edit) } : rule;
  }).filter((rule) => `${rule.id} ${rule.title} ${rule.summary} ${rule.pathPattern} ${rule.contentPattern}`.toLowerCase().includes(ruleSearch.toLowerCase()));
  const invalidRuleIds = [...new Set(activeWorkspaceIssues.map((issue) => issue.ruleId).filter((id): id is string => Boolean(id)))]
    .filter((id) => !state.rules.some((rule) => rule.id === id));
  const filteredInvalidRuleIds = invalidRuleIds.filter((id) => id.toLowerCase().includes(ruleSearch.toLowerCase()));
  const start = (limit: number) =>
    perform("実行開始", async () => {
      if (targetFolderError) throw new Error("対象フォルダのエラーを解消してから実行してください。");
      const next = await validateConfirmedSelection();
      if (!next.tasks.some((item) => item.file === selectedFile && !item.excluded))
        setSelectedFile(next.tasks.find((item) => !item.excluded)?.file || "");
      setDetailTab("diff");
      await api.Start(limit);
      setPage("results");
      acceptState(await api.GetState());
    });
  const retry = (files: string[]) =>
    perform("再実行の準備", async () => {
      if (targetFolderError) throw new Error("対象フォルダのエラーを解消してから再試行してください。");
      await flushWorkspaceChanges();
      acceptState(await api.RetryTasks(files));
      setFileSelection(null);
      if (files[0]) setSelectedFile(files[0]);
      setNotice({
        type: "success",
        message: `${number(files.length)} ファイルを再試行に追加しました。`,
      });
    });
  function beginDiscard(file: string) {
    if (locked || !state.tasks.some(task => task.file === file && task.canDiscardChanges)) return;
    setDiscardError("");
    setDiscardTarget({ file, workspaceId: state.activeWorkspaceId });
  }
  async function confirmDiscard() {
    if (!discardTarget || locked) return;
    if (discardTarget.workspaceId !== state.activeWorkspaceId) {
      setDiscardError("ワークスペースが切り替わりました。対象ファイルを選び直してください。");
      return;
    }
    setBusy("変更破棄");
    setDiscardError("");
    try {
      const next = await api.DiscardFileChanges(discardTarget.file);
      acceptState(next);
      setSelectedFile(discardTarget.file);
      setDetailTab("history");
      setFileSelection(previous => previous ? new Set([...previous, discardTarget.file]) : null);
      setDiscardTarget(null);
      setNotice({ type: "success", message: "変更を破棄し、ファイルを再試行に追加しました。" });
    } catch (error) {
      setDiscardError(errorMessage(error));
    } finally {
      setBusy("");
    }
  }
  const savedConnection = state.llmConnections.find((connection) => connection.id === editingConnectionId);
  const connectionDraft = connectionDrafts[editingConnectionId] || savedConnection || emptyLLMConnection;
  const connectionDirty = Boolean(connectionDrafts[editingConnectionId]);
  const personalLocked = Boolean(busy) || state.running || (!usable && !isPreview);
  const providerName = connectionDraft.provider === "openai" ? "OpenAI" : connectionDraft.provider === "claude" ? "Claude" : "Azure";
  const connectionUsesOAuth = isOAuthConnection(connectionDraft);
  const oauthSessionAvailable = hasMatchingOAuthSession(connectionDraft, savedConnection);
  const oauthConfigIssue = oauthConfigurationIssue(connectionDraft);
  const connectionIncompleteReason = !connectionDraft.name.trim() ? "接続名を入力してください。"
    : !connectionDraft.endpoint.trim() ? "エンドポイントを入力してください。"
    : !connectionDraft.deployment.trim() ? "モデルのデプロイ名またはモデルを入力してください。"
    : oauthConfigIssue;
  const oauthSignInDisabledReason = !usable ? "サインインはデスクトップアプリで利用できます。"
    : personalLocked ? "処理が終わるまでお待ちください。" : connectionIncompleteReason;
  const credentialAvailable = Boolean(!connectionUsesOAuth && savedConnection?.credentialSet &&
    connectionDraft.provider === savedConnection.provider &&
    connectionDraft.endpoint === savedConnection.endpoint &&
    connectionDraft.authMode === savedConnection.authMode);
  const changeConnection = <K extends keyof LLMConnection>(key: K, value: LLMConnection[K]) => {
    setConnectionDrafts((previous) => ({
      ...previous,
      [editingConnectionId]: {
        ...connectionDraft, [key]: value,
        ...(key === "authMode" ? { credential: "", credentialSet: false, oauthUsername: "", oauthSignedIn: false } : {}),
      },
    }));
    if (key === "authMode") setShowCredential(false);
  };
  const changeProvider = (provider: LLMConnection["provider"]) => {
    setConnectionDrafts((previous) => ({
      ...previous,
      [editingConnectionId]: {
        ...connectionDraft, provider,
        endpoint: provider === "openai" ? "https://api.openai.com/v1/" : provider === "claude" ? "https://api.anthropic.com/" : "",
        deployment: "", authMode: "api_key", credential: "", credentialSet: false,
        oauthTenantId: "", oauthClientId: "", oauthUsername: "", oauthSignedIn: false,
      },
    }));
    setShowCredential(false);
  };
  const openConnection = (id: string) => {
    const open = () => {
      setEditingConnectionId(id);
      setShowCredential(false);
      setConfirmDeleteConnection(false);
      setPage("llm");
    };
    if (page === "llm") { open(); return; }
    void perform("設定を保存", async () => { await flushWorkspaceChanges(); open(); });
  };
  const discardConnectionDraft = () => {
    setConnectionDrafts((previous) => {
      const next = { ...previous };
      delete next[editingConnectionId];
      return next;
    });
    setShowCredential(false);
  };
  async function saveConnection() {
    const previousIds = new Set(state.llmConnections.map((connection) => connection.id));
    const next = await api.SaveLLMConnection({ ...connectionDraft, name: connectionDraft.name.trim() });
    const id = connectionDraft.id || next.llmConnections.find((connection) => !previousIds.has(connection.id))?.id;
    if (!id) throw new Error("保存したLLM接続を確認できませんでした。");
    acceptState(next);
    discardConnectionDraft();
    setEditingConnectionId(id);
    setNotice({ type: "success", message: isPreview ? "画面プレビューにLLM接続を反映しました。実際の設定は保存していません。" : "LLM接続をこの端末に保存しました。" });
    return id;
  }
  async function signInConnection() {
    const id = connectionDirty || !savedConnection ? await saveConnection() : savedConnection.id;
    oauthCancelRequested.current = false;
    setOAuthSigningIn(true);
    try {
      const next = await api.SignInLLMConnection(id);
      acceptState(next);
      setNotice({ type: "success", message: "Microsoftアカウントでサインインしました。" });
    } catch (error) {
      if (!oauthCancelRequested.current) throw error;
      setNotice({ type: "success", message: "サインインをキャンセルしました。" });
    } finally {
      setOAuthSigningIn(false);
      setOAuthCancelPending(false);
      oauthCancelRequested.current = false;
    }
  }
  async function cancelSignIn() {
    oauthCancelRequested.current = true;
    setOAuthCancelPending(true);
    try {
      await api.CancelLLMSignIn();
    } catch (error) {
      oauthCancelRequested.current = false;
      setOAuthCancelPending(false);
      setNotice({ type: "error", message: errorMessage(error) });
    }
  }
  const selectedConnection = state.llmConnections.find((connection) => connection.id === state.selectedLLMConnectionId);
  const missingConnection = !hasReadyLLMConnection(state);

  const activeWorkspace = state.workspaces.find(
    (workspace) => workspace.id === state.activeWorkspaceId,
  );
  const deleteDisabled = entryLocked || state.readOnly || dirty || hasRuleChanges;
  function beginDelete(kind: "workspace" | "rule") {
    if (!activeWorkspace || deleteDisabled) return;
    const id = kind === "workspace" ? activeWorkspace.id : selectedRule;
    if (!id || id === "__new") return;
    setDeleteError("");
    setDeleteTarget({ kind, id, workspaceId: activeWorkspace.id,
      name: kind === "workspace" ? activeWorkspace.name : `${id}${ruleDetail?.title ? ` ${ruleDetail.title}` : ""}` });
  }
  async function confirmDelete() {
    if (!deleteTarget || deleteDisabled) return;
    if (deleteTarget.workspaceId !== state.activeWorkspaceId) {
      setDeleteError("ワークスペースが切り替わりました。削除対象を選び直してください。");
      return;
    }
    setBusy("削除");
    setDeleteError("");
    try {
      ++ruleRequestVersion.current;
      await ruleSessionQueue.current.catch(() => {});
      await api.CloseRule();
      const next = deleteTarget.kind === "workspace"
        ? await api.DeleteWorkspace(deleteTarget.id)
        : await api.DeleteRule(deleteTarget.id);
      acceptState(next, true);
      setSelectedRule(next.rules[0]?.id || next.workspaces.find((workspace) => workspace.id === next.activeWorkspaceId)?.issues?.find((issue) => issue.ruleId)?.ruleId || "");
      setRuleDetail(null);
      setRuleEditor(null);
      setRuleError("");
      setDeleteTarget(null);
      setNotice({ type: "success", message: `「${deleteTarget.name}」を削除しました。` });
    } catch (error) {
      setDeleteError(errorMessage(error));
      setRuleReloadVersion((version) => version + 1);
    } finally {
      setBusy("");
    }
  }
  function applyIssueDestination(workspaceId: string, issue: WorkspaceIssue) {
    const destination = issue.page;
    setPage(destination);
    setRuleSearch("");
    if (issue.ruleId) setSelectedRule(issue.ruleId);
    if (issue.file) {
      selectTask(issue.file);
      setDetailTab(issue.section === "checks" ? "checks" : issue.section === "history" ? "history" : "diff");
    }
    setIssueDestination({ workspaceId, issue: { ...issue, page: destination } });
  }
  const navigateToIssue = (workspaceId: string, issue: WorkspaceIssue) =>
    perform("エラー箇所を表示", async () => {
      if (workspaceId === state.activeWorkspaceId) {
        if (issue.page !== page) await flushWorkspaceChanges();
        applyIssueDestination(workspaceId, issue);
        return;
      }
      await flushWorkspaceChanges();
      const next = await api.SelectWorkspace(workspaceId);
      pendingIssueRef.current = { workspaceId, issue };
      acceptState(next, true);
    });
  const stepIndex = steps.findIndex((step) => step.id === page);
  useEffect(() => {
    if (workspaceRef.current !== state.activeWorkspaceId) {
      workspaceRef.current = state.activeWorkspaceId;
      setPage("target");
      setSelectedFile(state.tasks.find((item) => !item.excluded)?.file || "");
      setFileSelection(null);
      setSelectionContext(null);
      setSelectedRule("");
      setRuleDetail(null);
      setLogsOpen(false);
      const destination = pendingIssueRef.current;
      pendingIssueRef.current = null;
      if (destination?.workspaceId === state.activeWorkspaceId) {
        applyIssueDestination(destination.workspaceId, destination.issue);
      }
    }
  }, [state.activeWorkspaceId, state.running, page]);
  useEffect(() => {
    mainContentRef.current?.scrollTo({ top: 0 });
  }, [page, state.activeWorkspaceId]);
  useEffect(() => {
    if (!issueDestination || issueDestination.workspaceId !== state.activeWorkspaceId || issueDestination.issue.page !== page) return;
    const frame = requestAnimationFrame(() => {
      const { issue } = issueDestination;
      const root = mainContentRef.current;
      const section = issue.section
        ? [...(root?.querySelectorAll<HTMLElement>("[data-issue-section]") || [])].find((element) => element.dataset.issueSection === issue.section)
        : undefined;
      const target = issue.ruleId ? document.getElementById("rule-error-details") || ruleSectionRef.current : issue.file ? document.getElementById("result-file-detail") || root : section || root;
      for (let element: HTMLElement | null = target || null; element && element !== root; element = element.parentElement) {
        if (element instanceof HTMLDetailsElement) element.open = true;
      }
      target?.scrollIntoView({ block: "nearest" });
      target?.focus({ preventScroll: true });
      setIssueDestination(null);
    });
    return () => cancelAnimationFrame(frame);
  }, [issueDestination, state.activeWorkspaceId, page]);
  const switchWorkspace = (id: string) =>
    perform("ワークスペース切替", async () => {
      await flushWorkspaceChanges();
      acceptState(await api.SelectWorkspace(id), true);
      setPage("target");
    });
  const beginCreateWorkspace = () => {
    setWorkspaceName("");
    setWorkspaceRoot("");
    setDemoProjectRoot("");
    setWorkspaceRootError("");
    setNotice(null);
    setWorkspaceDialog("create");
  };
  const chooseWorkspaceRoot = () =>
    perform("対象フォルダの確認", async () => {
      const root = await api.ChooseDirectory("root");
      if (!root) return;
      setWorkspaceRoot(root);
      setDemoProjectRoot("");
      setWorkspaceRootError("");
      try {
        const validated = await api.ValidateTargetFolder(root);
        setWorkspaceRoot(validated);
        if (!workspaceName.trim()) setWorkspaceName(basename(validated));
      } catch (error) {
        setWorkspaceRootError(errorMessage(error));
      }
    });
  const createDemoProject = () => perform("デモプロジェクト作成", async () => {
    if (!usable || state.running || workspaceDialog !== "create") return;
    const parent = await api.ChooseDirectory("demo");
    if (!parent) return;
    const root = await api.CreateDemoProject(parent);
    setDemoProjectRoot(root);
    setWorkspaceRoot(root);
    setWorkspaceRootError("");
    if (!workspaceName.trim()) setWorkspaceName("デモプロジェクト");
  });
  const chooseTargetFolder = () =>
    perform("対象フォルダの変更", async () => {
      const root = await api.ChooseDirectory("root");
      if (!root) return;
      await flushWorkspaceChanges();
      const workspaceId = state.activeWorkspaceId;
      setTargetSelection({ workspaceId, path: root, error: "" });
      let validated: string;
      try {
        validated = await api.ValidateTargetFolder(root);
      } catch (error) {
        setTargetSelection({ workspaceId, path: root, error: errorMessage(error) });
        return;
      }
      try {
        await ruleSessionQueue.current;
        await api.CloseRule();
        const next = await api.ChangeTargetFolder(validated);
        acceptState(next, true);
        setTargetSelection(null);
        setFileSelection(null);
        setSelectionContext(null);
        setSelectedFile("");
          setSelectedRule("");
        setRuleDetail(null);
        setRuleEditor(null);
        setRuleError("");
          setLogsOpen(false);
        if (next.config.root !== state.config.root) setNotice({ type: "success", message: "対象フォルダを変更しました。「確認」で実行設定で対象ファイルを更新してください。" });
      } catch (error) {
        setTargetSelection({ workspaceId, path: validated, error: errorMessage(error) });
        setRuleReloadVersion((version) => version + 1);
      }
    });
  const submitWorkspace = () => {
    if (entryLocked) return;
    return perform("ワークスペース保存", async () => {
      if (workspaceDialog === "create" && workspaceRootError) return;
      if (workspaceDialog === "create" && !workspaceRoot)
        throw new Error("最初に対象フォルダを選択してください。");
      await flushWorkspaceChanges();
      acceptState(
        workspaceDialog === "create"
          ? demoProjectRoot === workspaceRoot
            ? await api.CreateDemoWorkspace(workspaceName.trim(), workspaceRoot)
            : await api.CreateWorkspace(workspaceName.trim(), workspaceRoot)
          : workspaceDialog === "duplicate"
            ? await api.DuplicateWorkspace(workspaceName.trim())
            : await api.RenameWorkspace(workspaceName.trim()),
        true,
      );
      setWorkspaceDialog(null);
      if (workspaceDialog !== "rename") setPage("target");
    });
  };
  useEffect(() => {
    if (!workspaceDialog) return;
    const previous = document.activeElement as HTMLElement | null;
    return () => {
      if (previous?.isConnected) previous.focus();
      else workspaceAddRef.current?.focus();
    };
  }, [workspaceDialog]);
  const rulesAvailable = state.rules.length > 0 || hasRuleChanges;
  const setupIssue = activeWorkspaceIssues.some((issue) => issue.page === "target" || issue.page === "rules");
  const reviewBlocked = !draft.root || Boolean(targetFolderError) || !rulesAvailable || Boolean(firstRuleDraftError) || (setupIssue && !hasRuleChanges);
  const selectedTargetFiles = fileSelection || new Set(state.tasks.filter((item) => !item.excluded).map((item) => item.file));
  const selectedTargetCount = state.tasks.filter(item => !isCompletedTask(item) && selectedTargetFiles.has(item.file)).length;
  const readyReviewCount = countReadyTargets(state.tasks, selectedTargetFiles);
  const selectionIsCurrent = selectionContext !== null && selectionContext === selectionContextKey(state.activeWorkspaceId, draft, state.rules);
  const navigationState = {
    busy: Boolean(busy),
    running: state.running,
    available: usable || isPreview,
    targetReady: Boolean(draft.root) && !targetFolderError,
    setupReady: !reviewBlocked,
    connectionReady: !missingConnection,
    readOnly: state.readOnly,
    selectionCurrent: selectionIsCurrent,
    readyCount: readyReviewCount,
    selectedCount: selectedTargetCount,
    selectedRuleCount: state.rules.filter(rule => !draft.excludedRuleIds?.includes(rule.id)).length,
    targetError: targetFolderError,
    setupError: firstRuleDraftError || activeWorkspaceIssues.find(issue => issue.page === "rules")?.message,
  };
  const navigation = workflowAvailability(navigationState);
  const navigationReasons = workflowBlockReasons(navigationState);
  const pendingSetupChanges = (dirty && !onlyConcurrencyChanged(draft, state.config)) || hasRuleChanges;
  const startDisabledReason = navigationReasons.review || (pendingSetupChanges ? "設定を保存して対象ファイルを確認してください。" : "");

  async function validateSetup(next: State, refresh = true): Promise<State> {
    if (!isPreview && refresh) next = await api.GetState();
    acceptState(next, true);
    const issues = next.workspaces.find((workspace) => workspace.id === next.activeWorkspaceId)?.issues || [];
    const issue = issues.find((item) => item.page === "target" || item.page === "rules");
    if (issue) { applyIssueDestination(next.activeWorkspaceId, issue); throw new Error(issue.message); }
    if (!next.config.root || !next.config.rulesPath || !next.rules.length) {
      setPage(!next.config.root ? "target" : "rules");
      throw new Error("対象フォルダとルールを設定してください。");
    }
    return next;
  }
  async function extractTargets(next: State) {
    setSelectionContext(null);
    next = await validateSetup(next);
    setPage("run");
    if (next.readOnly) return;
    next = await api.Scan();
    acceptState(next, true);
    setFileSelection(new Set(next.tasks.filter((item) => !item.excluded).map((item) => item.file)));
    setSelectionContext(selectionContextKey(next.activeWorkspaceId, next.config, next.rules));
  }
  const refreshTargets = () => perform("対象ファイルを更新", async () => {
    await extractTargets(await flushWorkspaceChanges());
  });
  async function validateConfirmedSelection(): Promise<State> {
    if (!selectionIsCurrent) {
      setPage("run");
      throw new Error("実行設定で対象ファイルを更新し、処理対象を確定してください。");
    }
    let next = await flushWorkspaceChanges();
    if (!isPreview) {
      try {
        // Reload the saved settings and queue, without scanning or adding files.
        next = await api.SelectWorkspace(next.activeWorkspaceId);
      } catch (error) {
        setSelectionContext(null);
        next = await api.GetState();
        acceptState(next, true);
        const issue = next.workspaces.find((workspace) => workspace.id === next.activeWorkspaceId)?.issues?.[0];
        if (issue) applyIssueDestination(next.activeWorkspaceId, issue);
        else setPage("run");
        throw error;
      }
    }
    next = await validateSetup(next, false);
    if (selectionContext !== selectionContextKey(next.activeWorkspaceId, next.config, next.rules)) {
      setSelectionContext(null);
      setPage("run");
      throw new Error("設定が変更されています。実行設定で対象ファイルを更新してください。");
    }
    if (next.readOnly) throw new Error("ワークスペースの編集権限が必要です。");
    if (!hasReadyLLMConnection(next)) {
      setPage("run");
      throw new Error("実行設定で利用できるLLM接続を選択してください。");
    }
    // A held file may already be checked in the saved queue. Confirming the
    // selection must request its resumable retry even when no checkbox changed.
    if (next.tasks.some(item => !item.excluded && item.status === "needs_human")) {
      next = await api.SetTaskSelection(next.tasks.filter(item => !item.excluded).map(item => item.file));
      acceptState(next);
    }
    if (!countReadyTargets(next.tasks)) {
      setPage("run");
      throw new Error("実行設定で処理対象ファイルを選択してください。");
    }
    return next;
  }
  const navigatePage = (destination: WorkflowPage) => {
    if (page === "rules") revealRuleDraftErrors();
    if (!navigation[destination] || destination === page) return;
    return perform(destination === "run" ? "対象ファイルを抽出" : "設定を保存", async () => {
      if (destination === "review") {
        await validateConfirmedSelection();
      } else {
        const next = await flushWorkspaceChanges();
        if (destination === "run") { await extractTargets(next); return; }
      }
      setPage(destination);
    });
  };
  const nextStep = () => navigatePage(steps[Math.min(steps.length - 1, stepIndex + 1)].id);

  return (
    <div className="app-shell">
      <aside className="sidebar workspace-sidebar">
        <div className="brand">
          <div className="brand-mark">
            <span />
            <span />
          </div>
          <div>
            <strong>
              OneByOne<span className="brand-dot">.</span>
            </strong>
          </div>
        </div>
        <section className="connection-sidebar" aria-label="LLM接続">
          <div className="workspace-list-heading">
            <button className="sidebar-section-label sidebar-section-button" disabled={personalLocked}
              onClick={() => openConnection(state.llmConnections[0]?.id || "__new")}>LLM接続</button>
            <button className="icon-button" aria-label="LLM接続を追加" disabled={personalLocked} onClick={() => openConnection("__new")}>
              <Icon name="plus" size={17} />
            </button>
          </div>
          <nav className="workspace-nav connection-nav" aria-label="LLM接続一覧">
            {state.llmConnections.map((connection) => (
              <button key={connection.id} title={connection.name}
                className={`workspace-name ${page === "llm" && editingConnectionId === connection.id ? "active" : ""}`}
                aria-current={page === "llm" && editingConnectionId === connection.id ? "page" : undefined}
                disabled={personalLocked} onClick={() => openConnection(connection.id)}>
                {connection.name}{connectionDrafts[connection.id] && <span className="rule-draft-icon" role="img" aria-label="未保存の変更"><Icon name="edit" size={14} /></span>}
              </button>
            ))}
            {(page === "llm" && editingConnectionId === "__new") || connectionDrafts.__new ? (
              <button className={`workspace-name ${page === "llm" && editingConnectionId === "__new" ? "active" : ""}`} disabled={personalLocked} onClick={() => openConnection("__new")}>
                新しいLLM接続{connectionDrafts.__new && <span className="rule-draft-icon" role="img" aria-label="未保存の変更"><Icon name="edit" size={14} /></span>}
              </button>
            ) : !state.llmConnections.length ? <p className="workspace-empty-label">接続はまだありません</p> : null}
          </nav>
        </section>
        <div className="workspace-list-heading">
          <span className="sidebar-section-label">ワークスペース</span>
          <button
            ref={workspaceAddRef}
            className="icon-button"
            aria-label="新しいワークスペースを作成"
            disabled={entryLocked}
            onClick={beginCreateWorkspace}
          >
            <Icon name="plus" size={17} />
          </button>
        </div>
        <nav className="workspace-nav" aria-label="ワークスペース一覧">
          {state.workspaces.map((workspace) => (
            <div key={workspace.id} className={`workspace-nav-item ${page !== "llm" && state.activeWorkspaceId === workspace.id ? "active" : ""}`}>
            <button
              title={workspace.name}
              className={`workspace-name ${page !== "llm" && state.activeWorkspaceId === workspace.id ? "active" : ""}`}
              aria-current={
                page !== "llm" && state.activeWorkspaceId === workspace.id ? "page" : undefined
              }
              disabled={
                Boolean(busy) || state.running || (!usable && !isPreview)
              }
              onClick={() => switchWorkspace(workspace.id)}
            >
              {workspace.name}
            </button>
            <WorkspaceIssues workspaceName={workspace.name} issues={targetSelection?.workspaceId === workspace.id && targetSelectionIssue
              ? [...(workspace.issues || []).filter((issue) => issue.page !== "target"), targetSelectionIssue]
              : workspace.issues || []}
              disabled={entryLocked} onSelect={(issue) => navigateToIssue(workspace.id, issue)} />
            </div>
          ))}
          {!state.workspaces.length && (
            <p className="workspace-empty-label">
              ワークスペースはまだありません
            </p>
          )}
        </nav>
      </aside>

      <div className="main-shell">
        {isPreview && (
          <div className="preview-banner">
            <Icon name="eye" size={15} />
            <strong>画面プレビュー</strong>
            <span>
              サンプルデータを表示中です。AI実行・ファイル操作は行いません。
            </span>
          </div>
        )}
        {!isNative && !isPreview && !loading && (
          <div className="preview-banner browser-banner">
            <Icon name="info" size={16} />
            <span>専用デスクトップアプリで起動してください。</span>
            <a href="?demo=1">
              画面プレビューを開く <Icon name="arrow" size={14} />
            </a>
          </div>
        )}
        <header className="topbar workspace-topbar">
          <div className="breadcrumb">
            <span className="workspace-current-name">
              {page === "llm" ? "LLM接続" : activeWorkspace?.name || "ワークスペースを始める"}
            </span>
            {activeWorkspace && page !== "llm" && (
              <code
                className="workspace-id"
                title={`ワークスペースID: ${activeWorkspace.id}`}
              >
                {activeWorkspace.id}
              </code>
            )}
            {activeWorkspace && page !== "llm" && (
              <button
                className="text-button workspace-rename"
                disabled={locked}
                onClick={() => {
                  setWorkspaceName(activeWorkspace.name);
                  setWorkspaceDialog("rename");
                }}
              >
                名前を変更
              </button>
            )}
          </div>
          <div className="topbar-right">
            {activeWorkspace && page !== "llm" && (
              <Button
                icon="cube"
                className="small"
                disabled={locked}
                onClick={() => {
                  setWorkspaceName(`${activeWorkspace.name} のコピー`);
                  setWorkspaceDialog("duplicate");
                }}
              >
                設定を複製
              </Button>
            )}
            {activeWorkspace && page !== "llm" && (
              <button className="icon-button danger-icon" aria-label="ワークスペースを削除" title={deleteDisabled ? "未保存の変更や実行中の処理がある場合は削除できません" : "ワークスペースを削除"}
                disabled={deleteDisabled} onClick={() => beginDelete("workspace")}>
                <Icon name="trash" size={17} />
              </button>
            )}
            <button
              className="icon-button"
              title="最新の状態を取得"
              aria-label="最新の状態を取得"
              disabled={Boolean(busy) || loading}
              onClick={() =>
                perform("更新", async () => acceptState(
                  page !== "llm" && state.readOnly && state.activeWorkspaceId
                    ? await api.SelectWorkspace(state.activeWorkspaceId)
                    : await api.GetState(),
                  page !== "llm" && state.readOnly,
                ))
              }
            >
              <Icon name="refresh" size={16} />
            </button>
          </div>
        </header>
        {activeWorkspace && page !== "llm" && (
          <nav className="workflow-tabs" style={{ gridTemplateColumns: `repeat(${steps.length}, minmax(80px, 1fr))` }} role="tablist" aria-label="処理の手順">
            {steps.map((step, index) => (
              <HoverTip key={step.id} reason={navigationReasons[step.id]} className="workflow-step-tip"><button
                id={`step-${step.id}`}
                role="tab"
                aria-selected={page === step.id}
                aria-controls="workflow-panel"
                className={`${page === step.id ? "active" : ""} ${stepIndex > index ? "visited" : ""}`}
                disabled={!navigation[step.id]}
                onClick={() => navigatePage(step.id)}
              >
                <span className="step-number">
                  {String(index + 1).padStart(2, "0")}
                </span>
                <span>{step.label}</span>
              </button></HoverTip>
            ))}
          </nav>
        )}
        {activeWorkspace && page !== "llm" && state.readOnly && (
          <div className="notice workspace-lock-notice" role="status">
            <Icon name="eye" size={17} />
            <div>
              <strong>別のアプリが開いています</strong>
              <span>
                {[state.workspaceLock?.owner, state.workspaceLock?.host].filter(Boolean).join(" · ") || "別のアプリ"}
                {" が編集中です。現在は閲覧専用で開いています。"}
              </span>
            </div>
            <Button className="small" disabled={Boolean(busy)} onClick={() => switchWorkspace(state.activeWorkspaceId)}>
              編集できるか確認
            </Button>
          </div>
        )}
        <main
          ref={mainContentRef}
          id="workflow-panel"
          tabIndex={-1}
          role={page === "llm" ? undefined : "tabpanel"}
          aria-labelledby={activeWorkspace && page !== "llm" ? `step-${page}` : undefined}
          className={`main-content page-${page}`}
        >
          {page === "llm" ? (
<>
                  <div className="page-heading">
                    <div>
                      <div className="title-with-help">
                        <h1>{savedConnection ? savedConnection.name : "新しいLLM接続"}</h1>
                        <HelpTip label="LLM接続">接続をこの端末に登録し、各ワークスペースの実行設定で選択します。</HelpTip>
                      </div>
                    </div>
                  </div>
                  <div className="settings-content">

                    <section className="llm-connection-form settings-section-fields" aria-label="LLM接続の設定">
                      <Field label="LLMプロバイダー" hint={`対象ファイル、必要なルール、検証エラーを、選択した${providerName}の接続先へ送信します。`}>
                        <select
                          value={connectionDraft.provider}
                          disabled={personalLocked}
                          onChange={(event) =>
                            changeProvider(
                              event.target.value as LLMConnection["provider"],
                            )
                          }
                        >
                          <option value="openai">OpenAI</option>
                          <option value="azure">Azure OpenAI / Microsoft Foundry</option>
                          <option value="claude">Claude</option>
                        </select>
                      </Field>
                      <Field label="接続名">
                        <input value={connectionDraft.name} disabled={personalLocked}
                          onChange={(event) => changeConnection("name", event.target.value)}
                          placeholder="例：開発用 OpenAI" autoComplete="off" />
                      </Field>
                      <Field
                        label="エンドポイント"
                        hint={
                          connectionDraft.provider === "azure"
                            ? "AzureのリソースURL、または /openai/v1/ で終わるAPIのベースURLを指定します。"
                            : connectionDraft.provider === "openai"
                              ? "OpenAI APIのベースURLです。必要な場合のみ変更してください。"
                              : "Anthropic APIのベースURLです。必要な場合のみ変更してください。"
                        }
                      >
                        <input
                          type="url"
                          value={connectionDraft.endpoint}
                          disabled={personalLocked}
                          onChange={(e) =>
                            changeConnection("endpoint", e.target.value)
                          }
                          placeholder={
                            connectionDraft.provider === "azure"
                              ? "https://your-resource.openai.azure.com/openai/v1/"
                              : connectionDraft.provider === "openai"
                                ? "https://api.openai.com/v1/"
                                : "https://api.anthropic.com/"
                          }
                          spellCheck={false}
                          autoComplete="off"
                        />
                      </Field>
                      <Field
                        label={
                          connectionDraft.provider === "azure"
                            ? "モデルのデプロイ名"
                            : "モデル"
                        }
                        hint={
                          connectionDraft.provider === "azure"
                            ? "モデル名ではなく、Azure上で設定したデプロイ名です。"
                            : "利用するモデルIDを入力してください。"
                        }
                      >
                        <input
                          value={connectionDraft.deployment}
                          disabled={personalLocked}
                          onChange={(e) =>
                            changeConnection("deployment", e.target.value)
                          }
                          placeholder={
                            connectionDraft.provider === "azure"
                              ? "migration-model"
                              : "モデルIDを入力"
                          }
                          spellCheck={false}
                          autoComplete="off"
                        />
                      </Field>
                      {connectionDraft.provider === "azure" && (
                        <Field label="認証方式">
                          <select
                            value={connectionDraft.authMode}
                            disabled={personalLocked}
                            onChange={(e) =>
                              changeConnection("authMode", e.target.value)
                            }
                          >
                            <option value="api_key">APIキー</option>
                            <option value="oauth">Microsoft Entra ID（OAuth）</option>
                            <option value="bearer">
                              Microsoft Entra ID アクセストークン
                            </option>
                          </select>
                        </Field>
                      )}
                      {connectionUsesOAuth ? <>
                        <Field label="テナントID" hint="Azureリソースのディレクトリ（テナント）IDを入力します。サインインするユーザーには、対象リソースへのモデル利用権限が必要です。">
                          <input value={connectionDraft.oauthTenantId || ""} disabled={personalLocked}
                            onChange={event => changeConnection("oauthTenantId", event.target.value)}
                            placeholder="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" autoComplete="off" spellCheck={false} />
                        </Field>
                        <Field label="アプリケーション（クライアント）ID" hint="Microsoft Entraのアプリ登録で発行したIDです。認証のプラットフォームに「モバイルとデスクトップ アプリケーション」を追加し、リダイレクトURIに http://localhost を登録します。クライアントシークレットは不要です。">
                          <input value={connectionDraft.oauthClientId || ""} disabled={personalLocked}
                            onChange={event => changeConnection("oauthClientId", event.target.value)}
                            placeholder="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" autoComplete="off" spellCheck={false} />
                        </Field>
                        <div className="oauth-account-panel" aria-live="polite">
                          <div className="oauth-account-status">
                            <Icon name={oauthSessionAvailable ? "check" : "link"} size={16} />
                            <span>{oauthSigningIn ? "ブラウザーでサインインを完了してください。"
                              : oauthSessionAvailable ? savedConnection?.oauthUsername || "サインイン済み" : "未サインイン"}</span>
                            <HelpTip label="Microsoftアカウントでサインイン">ブラウザーで認証します。認証トークンは暗号化してこの端末に保存し、必要に応じて自動更新します。サインアウトすると、このアプリに保存した認証情報を削除します。</HelpTip>
                          </div>
                          <div className="settings-inline-actions">
                            <Button icon="link" disabled={Boolean(oauthSignInDisabledReason)} disabledReason={oauthSignInDisabledReason}
                              loading={oauthSigningIn} onClick={() => perform("Microsoftサインイン", signInConnection)}>
                              {oauthSessionAvailable ? "別のアカウントでサインイン" : "Microsoftでサインイン"}
                            </Button>
                            {oauthSigningIn ? <Button className="small" disabled={oauthCancelPending} loading={oauthCancelPending} onClick={() => { void cancelSignIn(); }}>キャンセル</Button>
                              : oauthSessionAvailable && <Button className="small" disabled={personalLocked || !usable} disabledReason={!usable ? "サインアウトはデスクトップアプリで利用できます。" : "処理が終わるまでお待ちください。"}
                                onClick={() => perform("Microsoftサインアウト", async () => {
                                  acceptState(await api.SignOutLLMConnection(savedConnection!.id));
                                  setNotice({ type: "success", message: "この接続からサインアウトしました。" });
                                })}>サインアウト</Button>}
                          </div>
                        </div>
                      </> : <Field
                        label={
                          connectionDraft.provider === "azure" &&
                          connectionDraft.authMode === "bearer"
                            ? "アクセストークン"
                            : "APIキー"
                        }
                        hint="認証情報は暗号化して、この端末の個人設定に保存します。プロバイダー・接続先・認証方式が同じ場合、空欄のまま保存すると保存済みの認証情報を維持します。"
                      >
                        <div className="credential-field">
                          <input
                            type={showCredential ? "text" : "password"}
                            value={connectionDraft.credential}
                            disabled={personalLocked}
                            onChange={(e) =>
                              changeConnection("credential", e.target.value)
                            }
                            placeholder={
                              credentialAvailable
                                ? "認証情報を設定済み（変更する場合のみ入力）"
                                : "認証情報を入力"
                            }
                            autoComplete="off"
                            spellCheck={false}
                          />
                          <button
                            className="icon-button"
                            type="button"
                            onClick={() => setShowCredential(!showCredential)}
                            aria-label={
                              showCredential
                                ? "認証情報を隠す"
                                : "認証情報を表示"
                            }
                          >
                            <Icon name="eye" size={17} />
                          </button>
                        </div>
                      </Field>}
                      {credentialAvailable && (
                        <div className="settings-inline-actions credential-remove-action">
                          <button
                            type="button"
                            className="text-button"
                            disabled={personalLocked || connectionDirty}
                            onClick={() =>
                              perform("認証情報を削除", async () => {
                                acceptState(await api.ClearLLMConnectionCredential(savedConnection!.id));
                                setShowCredential(false);
                                setNotice({
                                  type: "success",
                                  message: isPreview ? "画面プレビューの認証情報を削除しました。" : "この端末に保存した認証情報を削除しました。",
                                });
                              })
                            }
                          >
                            {busy === "認証情報を削除" ? "削除中…" : "保存済みの認証情報を削除"}
                          </button>
                          <HelpTip label="保存済みの認証情報を削除">
                            このアプリが端末に保存したAPIキーやアクセストークンを削除します。
                            環境変数に認証情報が設定されている場合は、削除後もその認証情報を利用します。
                            未保存の変更がある場合は、先に保存するか変更を破棄してください。
                          </HelpTip>
                        </div>
                      )}
                      <div className="settings-inline-actions connection-save-actions">
                        <Button className="primary" icon="check" loading={busy === "接続保存"}
                          disabled={personalLocked || Boolean(connectionIncompleteReason) || (!connectionDirty && Boolean(savedConnection))}
                          disabledReason={personalLocked ? "処理が終わるまでお待ちください。" : connectionIncompleteReason || "保存済みです。"}
                          onClick={() => perform("接続保存", async () => { await saveConnection(); })}>
                          接続を保存
                        </Button>
                        {connectionDirty && <button className="text-button" disabled={personalLocked} onClick={discardConnectionDraft}>変更を破棄</button>}
                        <Button
                          icon="link"
                          disabled={
                            personalLocked || !usable || Boolean(connectionIncompleteReason) ||
                            (connectionUsesOAuth ? !oauthSessionAvailable : !connectionDraft.credential && !credentialAvailable)
                          }
                          disabledReason={!usable ? "接続テストはデスクトップアプリで利用できます。" : personalLocked ? "処理が終わるまでお待ちください。" : connectionIncompleteReason || (connectionUsesOAuth ? "Microsoftでサインインしてください。" : "認証情報を入力してください。")}
                          loading={busy === "接続テスト"}
                          onClick={() =>
                            perform("接続テスト", async () => {
                              const id = connectionDirty || !savedConnection ? await saveConnection() : savedConnection.id;
                              setNotice({
                                type: "success",
                                message: await api.TestLLMConnection(id),
                              });
                            })
                          }
                        >
                          接続をテスト
                        </Button>
                        <HelpTip label="接続をテスト">
                          未保存の変更がある場合は、設定を保存してから接続をテストします。
                          少量のAPI利用料金が発生する場合があります。
                        </HelpTip>
                      </div>
                    </section>
                    {savedConnection && (
                      <div className="connection-delete-actions">
                        {confirmDeleteConnection ? <>
                          <span>「{savedConnection.name}」をこの端末から削除しますか？</span>
                          <Button className="danger small" disabled={personalLocked} onClick={() => perform("接続削除", async () => {
                            const next = await api.DeleteLLMConnection(savedConnection.id);
                            acceptState(next);
                            discardConnectionDraft();
                            openConnection(next.llmConnections[0]?.id || "__new");
                            setNotice({ type: "success", message: isPreview ? "画面プレビューのLLM接続を削除しました。" : "LLM接続を削除しました。" });
                          })}>削除する</Button>
                          <Button className="small" disabled={personalLocked} onClick={() => setConfirmDeleteConnection(false)}>戻る</Button>
                        </> : <button className="text-button" disabled={personalLocked} onClick={() => setConfirmDeleteConnection(true)}><Icon name="trash" size={14} />接続を削除</button>}
                      </div>
                    )}
                  </div>
                </>
          ) : !activeWorkspace ? (
            <section className="workspace-welcome">
              <div className="welcome-symbol">
                <Icon name="folder" size={34} />
              </div>
              <h1>ワークスペースから、始める。</h1>
              <p>
                対象フォルダとルールを設定して、
                <br />
                準備から結果確認まで順番に進められます。
              </p>
              <div className="onboarding-actions">
                <Button
                  className="primary"
                  icon="plus"
                  disabled={entryLocked}
                  onClick={beginCreateWorkspace}
                >
                  新しいワークスペース
                </Button>
              </div>
            </section>
          ) : (
            <>
              {page === "run" && (
                <>
                <div className="execution-primary-content">
                <div className="execution-settings">
                  <div className="execution-settings-controls">
                  <Field label="使用するLLM接続">
                    <select value={state.selectedLLMConnectionId} disabled={personalLocked || state.readOnly}
                      onChange={(event) => {
                        const id = event.currentTarget.value;
                        void perform("LLM接続選択", async () => {
                          await flushWorkspaceChanges();
                          acceptState(await api.SelectLLMConnection(id), true);
                        });
                      }}>
                      <option value="">LLM接続を選択</option>
                      {state.llmConnections.map((connection) => <option key={connection.id} value={connection.id}>{connection.name}</option>)}
                    </select>
                  </Field>
                  <ExecutionRuleSelection rules={state.rules} excludedIds={draft.excludedRuleIds || []}
                    disabled={entryLocked || state.readOnly} onChange={ids => updateDraft("excludedRuleIds", ids)} />
                  </div>
                  {missingConnection && <div className="run-connection-note">
                    <Icon name="info" size={15} />
                    <span>{state.llmConnections.length ? selectedConnection ? "接続のモデル・認証情報を設定してください。" : "LLM接続を選択してください。" : "LLM接続を登録してください。"}</span>
                    <button className="text-button" disabled={personalLocked} onClick={() => openConnection(selectedConnection?.id || state.llmConnections[0]?.id || "__new")}>接続設定を開く</button>
                  </div>}
                </div>
                {!selectionIsCurrent && <div className="selection-refresh-note" role="status"><Icon name="info" size={14} />設定が変わった場合は、対象ファイルを更新して選択してください。</div>}
                <TargetFilesPanel key={`${state.activeWorkspaceId}:${state.config.root}`} workspaceId={state.activeWorkspaceId} tasks={state.tasks} rules={state.rules} root={state.config.root}
                  selected={selectedTargetFiles} onSelection={setFileSelection}
                  onRetry={file => retry([file])} retryDisabled={entryLocked || state.readOnly || !usable || !selectionIsCurrent || Boolean(targetFolderError)}
                  retryDisabledReason={state.running ? "実行中は再試行に追加できません。" : busy ? "処理が終わるまでお待ちください。" : state.readOnly ? "このワークスペースは閲覧専用です。" : !usable ? "再試行への追加はデスクトップアプリで利用できます。" : targetFolderError ? "対象フォルダのエラーを解消してください。" : "対象ファイルを更新してから再試行に追加してください。"}
                  disabledReason={entryLocked ? state.running ? "実行中は対象を変更できません。" : "処理が終わるまでお待ちください。" : state.readOnly ? "このワークスペースは閲覧専用です。" : "対象ファイルを更新してから選択してください。"}
                  disabled={entryLocked || state.readOnly || !selectionIsCurrent}
                  action={<HoverTip reason={entryLocked || state.readOnly || reviewBlocked ? navigationReasons.run || (state.readOnly ? "このワークスペースは閲覧専用です。" : "設定のエラーを解消してください。") : ""}><button className="text-button" disabled={entryLocked || state.readOnly || reviewBlocked} onClick={refreshTargets}>
                    <Icon name="refresh" size={14} />対象を更新</button></HoverTip>} />
                </div>
                </>
              )}
              {page === "review" && <TargetFilesPanel key={`${state.activeWorkspaceId}:${state.config.root}`} workspaceId={state.activeWorkspaceId} tasks={state.tasks.filter((item) => selectedTargetFiles.has(item.file))} rules={state.rules} root={state.config.root}
                selected={selectedTargetFiles} onSelection={setFileSelection} disabled={entryLocked || state.readOnly} selectionEnabled={false}
                action={<HoverTip reason={navigationReasons.run}><button className="text-button" disabled={!navigation.run} onClick={() => navigatePage("run")}>対象を変更<Icon name="arrow" size={13} /></button></HoverTip>} />}
              {page === "results" && <ResultsPanel key={state.activeWorkspaceId} state={state} busy={Boolean(busy)} usable={usable} canSetup={navigation.run}
                selectedFile={selectedFile} onSelectFile={selectTask} detailTab={detailTab} onDetailTabChange={setDetailTab}
                onStop={() => perform("停止", async () => { await api.Stop(); acceptState(await api.GetState()); })}
                onExport={executionId => perform("結果出力", async () => {
                  const path = executionId ? await api.ExportExecutionReport(executionId) : await api.ExportReport();
                  if (path) setNotice({ type: "info", message: `レポートを保存しました: ${path}` });
                })}
                onRetry={retry} onDiscard={beginDiscard} onOpenWorktree={() => perform("作業コピーを開く", () => api.OpenWorktree())}
                onSetup={() => navigatePage("run")} />}
              {page === "publish" && <ResultPublicationPanel key={publicationDraftKey} state={state} busy={Boolean(busy)} usable={usable}
                initialDraft={publicationDrafts.current[publicationDraftKey]}
                onDraftChange={value => { publicationDrafts.current[publicationDraftKey] = value; }}
                onPublish={async request => {
                  let result: ResultPublication | undefined;
                  let failure: unknown;
                  await perform("結果反映", async () => {
                    try {
                      result = await api.PublishResults(request);
                      setNotice({ type: "success", message: `${result.branch} に1コミットで反映しました。` });
                    } catch (error) { failure = error; throw error; }
                  });
                  if (failure) throw failure;
                  if (!result) throw new Error("反映結果を取得できませんでした。反映履歴を確認してください。");
                  return result;
                }} />}

              {page === "rules" && (
                <>
                  <div className="rules-primary-content">
                  <section ref={ruleSectionRef} className="rules-workspace compact-rules-workspace" aria-label="ルール一覧と内容" tabIndex={-1} data-issue-section="package">
                    <div className="rules-list">
                      <div className="panel-title">
                        <div className="title-with-help">
                          <h2>ルール一覧 <span>{state.rules.length + invalidRuleIds.length + Number(Boolean(ruleDrafts[`${state.activeWorkspaceId}/__new`]))}</span></h2>
                          <HelpTip label="ルール一覧">
                            <p>対象ファイル・内容の条件が両方空欄のルールは、すべてのファイルに適用します。</p>
                            <p>条件を指定したルールは、一致するファイルだけを対象にします。ルールを選択して内容を確認・編集できます。</p>
                          </HelpTip>
                        </div>
                        <div className="rule-list-actions">
                          <button type="button" className="icon-button add-rule-button" aria-label="ルールを追加" title="ルールを追加" disabled={locked} onClick={beginNewRule}><Icon name="plus" size={17} /></button>
                          <RulePackageActions disabled={locked} exportDisabled={!state.rules.length && !hasRuleChanges}
                            disabledReason={busy ? "処理中です。完了までお待ちください。" : state.readOnly ? "このワークスペースは閲覧専用です。" : isPreview ? "画面プレビューではファイル操作はできません。" : undefined}
                            onImport={beginPackageImport} onExport={exportRulePackage} />
                        </div>
                      </div>
                      <div className="search-field">
                        <Icon name="search" size={16} />
                        <input aria-label="ルールを検索" value={ruleSearch} onChange={(e) => setRuleSearch(e.target.value)} placeholder="ID・名前で検索" />
                      </div>
                      <div className="rules-list-scroll" role="list" aria-label="ルール">
                        {ruleDrafts[`${state.activeWorkspaceId}/__new`] && (
                          <button className={`rule-list-item compact-rule-item ${selectedRule === "__new" ? "selected" : ""}`} disabled={Boolean(busy)} onClick={() => selectRule("__new")}>
                            <RuleScopeIcon rule={ruleDrafts[`${state.activeWorkspaceId}/__new`]} />
                            <span className="rule-id">{ruleDrafts[`${state.activeWorkspaceId}/__new`].id.trim() || "新規"}</span>
                            <strong>{ruleDrafts[`${state.activeWorkspaceId}/__new`].name || "新しいルール"}</strong><span className="rule-draft-icon" role="img" aria-label="未保存の変更"><Icon name="edit" size={14} /></span>
                            {ruleIssuesFor("__new").length > 0 && <span className="rule-list-error-icon" role="img" aria-label="入力エラー" title={ruleIssuesFor("__new").map(issue => issue.message).join("\n")}><Icon name="warning" size={15} /></span>}
                          </button>
                        )}
                        {filteredRules.map((rule) => (
                          <button key={rule.id} className={`rule-list-item compact-rule-item ${selectedRule === rule.id ? "selected" : ""}`}
                            disabled={Boolean(busy)} onClick={() => selectRule(rule.id)} title={`${rule.id} ${rule.title}`}>
                            <RuleScopeIcon rule={rule} />
                            <span className="rule-id" title={rule.id}>{rule.id}</span>
                            <strong>{ruleDrafts[`${state.activeWorkspaceId}/${rule.id}`]?.name ?? rule.title ?? rule.id}</strong>
                            {ruleDrafts[`${state.activeWorkspaceId}/${rule.id}`] && <span className="rule-draft-icon" role="img" aria-label="未保存の変更"><Icon name="edit" size={14} /></span>}
                            {ruleIssuesFor(rule.id).length > 0 && <span className="rule-list-error-icon" role="img" aria-label="入力エラー" title={ruleIssuesFor(rule.id).map(issue => issue.message).join("\n")}><Icon name="warning" size={15} /></span>}
                          </button>
                        ))}
                        {filteredInvalidRuleIds.map((id) => (
                          <button key={id} className={`rule-list-item compact-rule-item invalid-rule-item ${selectedRule === id ? "selected" : ""}`}
                            disabled={Boolean(busy)} onClick={() => openRule(id)} title={`${id} 読み込みエラー`}>
                            <Icon name="rules" size={16} />
                            <span className="rule-id" title={id}>{id}</span>
                            <strong>読み込みエラー</strong>
                            <Icon name="warning" size={15} />
                          </button>
                        ))}
                        {!filteredRules.length && !filteredInvalidRuleIds.length && !ruleDrafts[`${state.activeWorkspaceId}/__new`] && (
                          <div className="small-empty"><Icon name="rules" size={28} /><p>{state.rules.length ? "一致するルールがありません。" : "「…」からJSON・CSVを読み込むか、ルールを追加してください。"}</p></div>
                        )}
                      </div>
                    </div>
                    <div className="rule-reader">
                      {!ruleDetail && selectedRule !== "__new" && selectedRuleIssues.length > 0 && (
                        <>
                          <div className="rule-reader-header">
                            <div className="rule-reader-identity">
                              <Icon name="rules" size={17} />
                              <span className="rule-id" title={selectedRule}>{selectedRule}</span>
                              <strong>{selectedRuleSource?.title || "ルールを読み込めません"}</strong>
                              <WorkspaceIssues workspaceName={`ルール ${selectedRule === "__new" ? currentRuleDraft?.id.trim() || "新規" : selectedRule}`} issues={selectedRuleIssues}
                                onSelect={() => document.getElementById("rule-error-details")?.focus()} />
                            </div>
                            <button className="icon-button danger-icon" aria-label="ルールを削除" title="ルールを削除" disabled={deleteDisabled} onClick={() => beginDelete("rule")}><Icon name="trash" size={17} /></button>
                          </div>
                          <RuleErrorDetails issues={selectedRuleIssues} />
                        </>
                      )}
                      {ruleDetail || selectedRule === "__new" ? (
                        <>
                          <div className="rule-reader-header">
                            <div className="rule-reader-identity">
                              <RuleScopeIcon rule={currentRuleDraft ?? ruleDetail ?? {}} />
                              <span className="rule-id" title={ruleDetail?.id}>{selectedRule === "__new" ? currentRuleDraft?.id.trim() || "新規" : ruleDetail?.id}</span>
                              <strong title={currentRuleDraft?.name ?? ruleDetail?.title}>{(currentRuleDraft?.name ?? ruleDetail?.title)?.trim() || "名称未入力"}</strong>
                              <WorkspaceIssues workspaceName={`ルール ${selectedRule === "__new" ? currentRuleDraft?.id.trim() || "新規" : selectedRule}`} issues={selectedRuleIssues}
                                onSelect={() => document.getElementById("rule-error-details")?.focus()} />
                            </div>
                            <div className="rule-header-controls">
                              <button className={`icon-button${currentRuleDraft ? "" : " invisible-control"}`} aria-label="ルールの変更を取り消す" title="変更を取り消す" disabled={Boolean(busy) || !currentRuleDraft} onClick={discardRuleDraft}><Icon name="close" size={16} /></button>
                              <button className={`icon-button danger-icon${selectedRule === "__new" ? " invisible-control" : ""}`} aria-label="ルールを削除" title="ルールを削除"
                                disabled={selectedRule === "__new" || deleteDisabled || (!isPreview && Boolean(ruleEditor?.readOnly))} onClick={() => beginDelete("rule")}><Icon name="trash" size={17} /></button>
                            </div>
                          </div>
                          {selectedRuleIssues.length > 0 && <RuleErrorDetails issues={selectedRuleIssues} />}
                          {ruleEditor?.readOnly && (
                            <div className="rule-readonly-note" role="status"><Icon name="eye" size={14} />
                              {isPreview ? "画面プレビューでは閲覧のみできます。" : [ruleEditor.lockOwner,ruleEditor.lockHost].filter(Boolean).length ? `${[ruleEditor.lockOwner,ruleEditor.lockHost].filter(Boolean).join(" · ")} が編集中です。閲覧専用で開いています。` : "このルールは閲覧専用で開いています。"}
                            </div>
                          )}
                          {staleRuleDraft && <div className="rule-readonly-note" role="alert">
                            <Icon name="warning" size={14} /><span>このルールは更新されています。変更を取り消して最新の内容を確認してください。</span>
                            <button className="text-button" disabled={Boolean(busy)} onClick={discardRuleDraft}>取消して最新を表示</button>
                          </div>}
                          <div className="rule-edit-scroll rule-markdown-edit">
                            <div className="rule-metadata-grid">
                              <div className="rule-identity-fields">
                                <Field label="ID" hint="英数字で始まる64文字以内の英数字・ハイフン・アンダースコアで指定します。作成後は変更できません。">
                                  <input className="code-input" value={currentRuleDraft?.id ?? ruleDetail?.id ?? ""} readOnly={selectedRule !== "__new"} disabled={locked || Boolean(ruleEditor?.readOnly)}
                                    aria-invalid={selectedRule === "__new" && touchedRuleDrafts.has(ruleDraftKey) && Boolean(ruleIdError(currentRuleDraft?.id ?? "", takenRuleIds))}
                                    onBlur={() => touchRuleDraft()} onChange={e => changeRuleDraft("id", e.target.value)} spellCheck={false} placeholder="1" />
                                </Field>
                                <Field label="名称">
                                  <input value={currentRuleDraft?.name ?? ruleDetail?.title ?? ""} disabled={locked || Boolean(ruleEditor?.readOnly)}
                                    aria-invalid={touchedRuleDrafts.has(ruleDraftKey) && !(currentRuleDraft?.name ?? ruleDetail?.title ?? "").trim()}
                                    onBlur={() => touchRuleDraft()} onChange={e => changeRuleDraft("name", e.target.value)} placeholder="ルールの名前" />
                                </Field>
                              </div>
                              <Field label="説明" className="rule-metadata-wide" hint="AIがルールの内容を判断するための説明です。対象の型名や処理、変更の目的を簡潔に記載してください。">
                                <textarea value={currentRuleDraft?.description ?? ruleDetail?.summary ?? ""} rows={2} disabled={locked || Boolean(ruleEditor?.readOnly)}
                                  aria-invalid={touchedRuleDrafts.has(ruleDraftKey) && !(currentRuleDraft?.description ?? ruleDetail?.summary ?? "").trim()}
                                  onBlur={() => touchRuleDraft()} onChange={e => changeRuleDraft("description", e.target.value)} placeholder="どのような変更をするルールか" />
                              </Field>
                              <Field label="対象ファイル" hint="対象フォルダからの相対パスをglobで指定します。例: src/**/*.tsx。空欄なら全ファイル。内容の条件と両方指定した場合は、両方を満たすファイルに適用します。">
                                <input className="code-input" value={ruleValue("pathPattern")} disabled={locked || Boolean(ruleEditor?.readOnly)} onChange={e => changeRuleDraft("pathPattern", e.target.value)} spellCheck={false} placeholder="全ファイル" />
                              </Field>
                              <Field label="内容の条件" hint="ファイル内容に照合する正規表現です。空欄なら内容で絞り込みません。">
                                <input className="code-input" value={ruleValue("contentPattern")} disabled={locked || Boolean(ruleEditor?.readOnly)} onChange={e => changeRuleDraft("contentPattern", e.target.value)} spellCheck={false} placeholder="絞り込みなし" />
                              </Field>
                            </div>
                            <RuleBodyEditor body={ruleValue("body")} readOnly={locked || Boolean(ruleEditor?.readOnly)} onChange={body => changeRuleDraft("body", body)} />
                            {ruleDetail && <div className="rule-retry-action action-with-help"><Button className="small" icon="refresh" disabled={locked || dirty || hasRuleChanges || Boolean(targetFolderError) || !state.tasks.some(item => item.rulesApplied.includes(ruleDetail.id))}
                              onClick={() => retry(state.tasks.filter(item => item.rulesApplied.includes(ruleDetail.id)).map(item => item.file))}>適用済みを再試行</Button><HelpTip label="適用済みを再試行">このルールを適用したファイルだけを再試行します。</HelpTip></div>}
                          </div>
                        </>
                      ) : selectedRuleIssues.length === 0 && (
                        <div className="detail-empty"><div className="empty-icon"><Icon name="rules" size={29} /></div><strong>{selectedRule ? "ルールを読み込み中…" : "ルールを選択してください"}</strong><p>名称・説明・対象の条件とMarkdown本文を確認・編集できます。</p></div>
                      )}
                    </div>
                  </section>
                  </div>
                </>
              )}

              {page === "target" && (
                <div className="settings-content target-folder-content" data-issue-section="target" tabIndex={-1}>
                  <Field label="対象フォルダ" hint="選択時にGit管理・初回コミット・リポジトリ全体の未コミット変更を確認します。変更後は対象ファイルの再抽出が必要です。以前の実行結果は保持されます。">
                    <div className={`path-field target-root-field${targetFolderError ? " path-field-error" : ""}`}>
                      <input value={selectedTarget?.path || activeWorkspace?.root || draft.root} readOnly aria-readonly="true"
                        aria-invalid={Boolean(targetFolderError)} aria-describedby={targetFolderError ? "target-folder-error" : undefined} />
                      <button type="button" disabled={locked} onClick={chooseTargetFolder} aria-label="対象フォルダを変更">変更</button>
                    </div>
                  </Field>
                  {targetFolderError && <div id="target-folder-error" className="field-validation-error" role="alert"><Icon name="warning" size={15} /><span>{targetFolderError}</span></div>}
                  {selectedTarget?.error && <button type="button" className="text-button target-selection-cancel" disabled={Boolean(busy)} onClick={() => setTargetSelection(null)}>変更を取り消す</button>}
                  {!selectedTarget?.error && state.config.root && <TargetFolderBrowser key={`${state.activeWorkspaceId}:${state.config.root}`} workspaceId={state.activeWorkspaceId} root={state.config.root} />}
                </div>
              )}
            </>
          )}
        </main>
        {activeWorkspace && page !== "llm" && (
          <div className="workflow-footer" onPointerDownCapture={() => { if (page === "rules") revealRuleDraftErrors(); }} onFocusCapture={() => { if (page === "rules") revealRuleDraftErrors(); }}>
            <div>
              {stepIndex > 0 && <Button className="back-button" disabled={!navigation[steps[stepIndex - 1].id]}
                disabledReason={navigationReasons[steps[stepIndex - 1].id]}
                onClick={() => navigatePage(steps[stepIndex - 1].id)}><Icon name="chevron" className="back-chevron" size={14} />前へ</Button>}
              <span>{state.readOnly ? "閲覧専用" : (page === "run" || page === "review") ? `${number(selectedTargetCount)} 件選択 · 今回 ${number(readyReviewCount)} 件を処理` : dirty || hasRuleChanges ? "変更は移動時に保存されます" : ""}</span>
            </div>
            {page === "review" ? <div className="review-start-actions">
              <ConcurrencyControl value={draft.concurrency} disabled={entryLocked || state.readOnly} onChange={value => updateDraft("concurrency", value)} />
              <Button icon="play" className="primary" loading={busy === "実行開始"}
              disabled={locked || reviewBlocked || missingConnection || pendingSetupChanges || !selectionIsCurrent || !readyReviewCount}
              disabledReason={startDisabledReason || (isPreview ? "画面プレビューではAI処理を実行できません。" : "現在は実行できません。")}
              onClick={() => start(0)}>実行</Button></div> : stepIndex < steps.length - 1 && <Button icon="arrow" className="primary"
              disabled={!navigation[steps[stepIndex + 1].id]}
              disabledReason={navigationReasons[steps[stepIndex + 1].id]}
              onClick={nextStep}>次へ</Button>}
          </div>
        )}

        <footer className="statusbar">
          <span>
            <span
              className={`statusbar-dot ${state.running ? "running" : ""}`}
            />
            {loading
              ? "読み込み中"
              : busy || (state.running ? "処理を実行中" : gitSetup.checking ? "Gitを確認中…" : "準備完了")}
            {isPreview && " · 画面プレビュー"}
          </span>
          <span>
            {gitSetup.installation && !gitSetup.installation.available && (
              <button onClick={gitSetup.show} disabled={Boolean(busy) || state.running || gitSetup.checking}>
                <Icon name="warning" size={13} />Gitの準備
              </button>
            )}
            {state.config.deployment && (
              <>
                <Icon name="spark" size={12} />
                {state.config.deployment}
                <i />
              </>
            )}
            <button onClick={() => setLogsOpen(!logsOpen)}>
              <Icon name="terminal" size={13} />
              実行ログ
              <Icon name={logsOpen ? "down" : "chevron"} size={12} />
            </button>
          </span>
        </footer>
      </div>
      <Toast notice={workspaceDialog || packageImport || showGitSetup || discardTarget ? null : notice} onDismiss={dismissNotice} />
      {showGitSetup && gitSetup.installation && <GitSetupDialog
        installation={gitSetup.installation} checking={gitSetup.checking} error={gitSetup.error}
        onRetry={retryGitInstallation} onDownload={gitSetup.openGuide} onDismiss={gitSetup.dismiss} />}
      {packageImport && <RulePackageImportDialog path={packageImport.path} busy={Boolean(busy)} error={notice?.type === "error" ? notice.message : undefined}
        onCancel={() => { setPackageImport(null); setNotice(null); }}
        onConfirm={mode => perform("パッケージ読込", () => importRulePackage(packageImport.path, packageImport.workspaceId, mode))} />}
      {deleteTarget && <DeleteConfirmation kind={deleteTarget.kind} name={deleteTarget.name} busy={busy === "削除"}
        error={deleteError} onCancel={() => setDeleteTarget(null)} onConfirm={confirmDelete} />}
      {discardTarget && <DeleteConfirmation kind="changes" name={discardTarget.file} busy={Boolean(busy)}
        error={discardError} onCancel={() => setDiscardTarget(null)} onConfirm={confirmDiscard} />}
      {workspaceDialog && (
        <div
          className="modal-backdrop"
          onClick={(event) => {
            if (event.target === event.currentTarget && !busy)
              setWorkspaceDialog(null);
          }}
        >
          <form
            tabIndex={-1}
            className="workspace-dialog"
            onKeyDown={(event) => {
              if (event.key === "Escape" && !busy) setWorkspaceDialog(null);
              if (event.key !== "Tab") return;
              const controls = Array.from(event.currentTarget.querySelectorAll<HTMLElement>(
                'button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex="0"]',
              )).filter((element) => element.getClientRects().length > 0);
              const first = controls[0];
              const last = controls.at(-1);
              if (event.shiftKey && document.activeElement === first) {
                event.preventDefault();
                last?.focus();
              } else if (!event.shiftKey && document.activeElement === last) {
                event.preventDefault();
                first?.focus();
              }
            }}
            role="dialog"
            aria-modal="true"
            aria-labelledby="workspace-dialog-title"
            onSubmit={(e) => {
              e.preventDefault();
              submitWorkspace();
            }}
          >
            <div className="dialog-heading">
              <h2 id="workspace-dialog-title">
                {workspaceDialog === "create"
                  ? "新しいワークスペース"
                  : workspaceDialog === "duplicate"
                    ? "設定を複製"
                    : "ワークスペース名を変更"}
              </h2>
              <button
                type="button"
                className="icon-button"
                aria-label="閉じる"
                disabled={Boolean(busy)}
                onClick={() => setWorkspaceDialog(null)}
              >
                <Icon name="close" size={18} />
              </button>
            </div>
            {notice?.type === "error" && (
              <div className="inline-error" role="alert">{notice.message}</div>
            )}
            {workspaceDialog === "create" && (
              <Field label="対象フォルダ" hint="初回コミット済みで、リポジトリ全体に未コミット変更のないフォルダを選択します。サブフォルダも使用できます。作成後も対象フォルダ画面から変更できます。"
                action={<button type="button" className="text-button demo-link" disabled={entryLocked || !usable} onClick={createDemoProject}>
                  {busy === "デモプロジェクト作成" && <span className="spinner" />}デモプロジェクトを作成する
                </button>}>
                <div className={`path-field${workspaceRootError ? " path-field-error" : ""}`}>
                  <Icon name="folder" size={17} />
                  <input value={workspaceRoot} placeholder="最初にフォルダを選択" readOnly aria-invalid={Boolean(workspaceRootError)} aria-describedby={workspaceRootError ? "workspace-root-error" : undefined} />
                  <button type="button" autoFocus disabled={Boolean(busy)} onClick={chooseWorkspaceRoot} aria-label="作成する対象フォルダを選択">選択</button>
                </div>
                {workspaceRootError && <div id="workspace-root-error" className="field-validation-error" role="alert"><Icon name="warning" size={15} /><span>{workspaceRootError}</span></div>}
              </Field>
            )}
              <Field label="ワークスペース名" hint={workspaceDialog === "duplicate" ? "別のIDで設定をコピーします。実行結果は引き継ぎません。登録済みのLLM接続は同じ接続を選択します。" : "名前は後から変更できます。ワークスペースのIDは変更されません。"}>
                <input
                  autoFocus={workspaceDialog !== "create"}
                  maxLength={100}
                  value={workspaceName}
                  onChange={(e) => setWorkspaceName(e.target.value)}
                  placeholder="例：ストレージAPI更新"
                  disabled={Boolean(busy) || (workspaceDialog === "create" && !workspaceRoot)}
                />
              </Field>
            <div className="dialog-actions">
              <Button
                disabled={Boolean(busy)}
                onClick={() => setWorkspaceDialog(null)}
              >
                キャンセル
              </Button>
              <button
                type="submit"
                className="button primary"
                disabled={
                  entryLocked ||
                  !workspaceName.trim() ||
                  (workspaceDialog === "create" && (!workspaceRoot || Boolean(workspaceRootError))) ||
                  ((workspaceDialog === "rename" || workspaceDialog === "duplicate") && locked)
                }
              >
                {busy
                  ? "保存中…"
                  : workspaceDialog === "create"
                    ? "作成する"
                    : workspaceDialog === "duplicate"
                      ? "複製する"
                      : "名前を保存"}
              </button>
            </div>
          </form>
        </div>
      )}
      {logsOpen && (
        <section className="log-drawer" aria-label="実行ログ">
          <header>
            <h2>
              <Icon name="terminal" size={17} />
              実行ログ <span>{state.logs.length}</span>
            </h2>
            <button
              className="icon-button"
              onClick={() => setLogsOpen(false)}
              aria-label="実行ログを閉じる"
            >
              <Icon name="close" size={18} />
            </button>
          </header>
          <div className="log-entries">
            {state.logs.length ? (
              [...state.logs].reverse().map((log, i) => (
                <div
                  className={`log-entry log-${log.level}`}
                  key={`${log.time}-${i}`}
                >
                  <time>{time(log.time)}</time>
                  <span className="log-level">{log.level.toUpperCase()}</span>
                  <p>{log.message}</p>
                </div>
              ))
            ) : (
              <div className="small-empty">
                操作を開始すると、ログがここに表示されます。
              </div>
            )}
          </div>
        </section>
      )}
    </div>
  );
}
