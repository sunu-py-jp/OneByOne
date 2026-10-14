import { useEffect, useId, useRef, useState } from "react";
import { SolutionTracePanel } from "./SolutionTracePanel";
import { api } from "./bridge";
import { Icon } from "./icons";
import { ResultCode } from "./ResultsPanel";
import { executionLabel } from "./ExecutionResultsPanel";
import { useResultSnapshot } from "./useResultSnapshot";
import { selectedSolutionPaths, solutionAttemptIndex, solutionConfigurationIssue, solutionStatusLabel } from "./solution";
import { defaultConfig, type Config, type ExecutionRunResult, type FileDetail, type SolutionConfig, type State } from "./types";
import "./solution.css";

const message = (error: unknown) => error instanceof Error ? error.message : String(error);
const quantity = (value: number) => new Intl.NumberFormat("ja-JP").format(value || 0);
const reviewVerdict = (value: string) => ({ passed: "合格", passed_with_holds: "保留あり", needs_changes: "要修正", needs_human: "要確認", running: "レビュー中", error: "エラー" } as Record<string, string>)[value] || value;
const assessmentStatus = (value: string) => ({ satisfied: "適合", passed: "適合", applied: "適用済み", not_applicable: "対象外", needs_changes: "要修正", needs_human: "要確認", violated: "不適合", failed: "不適合" } as Record<string, string>)[value] || value;

export function WorkspaceModeSelect({ mode, disabled, onChange }: { mode: Config["mode"]; disabled: boolean; onChange: (mode: Config["mode"]) => void }) {
  const id = useId();
  return <div className="field solution-mode-select">
    <label className="field-label" htmlFor={id}>ワークスペースの処理モード</label>
    <select id={id} value={mode || "files"} disabled={disabled} onChange={event => onChange(event.target.value as Config["mode"])}>
      <option value="files">ファイル単位</option>
      <option value="solution">VB.NET ソリューション単位（VS2022）</option>
    </select>
    <p className="solution-hint">{mode === "solution" ? "ソリューションをビルドし、ルールとコンパイルエラーを基に複数ファイルを修正して、再ビルドを繰り返します。" : "対象ファイルを選択し、ファイルごとにルールに沿って修正します。"}</p>
  </div>;
}

export function SolutionSelection({ config, workspaceId, root, disabled, readOnly = false, selectionEnabled = true, onChange }: {
  config?: SolutionConfig; workspaceId: string; root: string; disabled: boolean; readOnly?: boolean; selectionEnabled?: boolean; onChange: (config: SolutionConfig) => void;
}) {
  const [solutions, setSolutions] = useState<string[]>([]);
  const [searching, setSearching] = useState(false);
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const latest = useRef({ config, onChange, readOnly });
  latest.current = { config, onChange, readOnly };
  useEffect(() => {
    let current = true;
    setSolutions([]); setError(""); setSearching(Boolean(root));
    if (root) api.DiscoverSolutions().then(found => {
      if (!current) return;
      const ordered = [...(found || [])].sort((a, b) => a < b ? -1 : a > b ? 1 : 0);
      setSolutions(ordered);
      const latestValue = { ...defaultConfig.solution!, ...latest.current.config };
      if (latestValue.paths == null && !latest.current.readOnly && ordered.length) {
        latest.current.onChange({ ...latestValue, path: "", paths: latestValue.path.trim() ? selectedSolutionPaths(latestValue) : ordered });
      }
    }).catch(failure => { if (current) setError(message(failure)); }).finally(() => { if (current) setSearching(false); });
    return () => { current = false; };
  }, [workspaceId, root, refresh]);
  const selected = selectedSolutionPaths(config);
  const listed = [...new Set([...solutions, ...selected])].sort((a, b) => a < b ? -1 : a > b ? 1 : 0);
  const select = (paths: string[]) => onChange({ ...defaultConfig.solution!, ...latest.current.config, path: "", paths: [...new Set(paths)].sort((a, b) => a < b ? -1 : a > b ? 1 : 0) });
  return <section className="solution-selection" aria-label="対象フォルダ内のソリューション">
    <div className="solution-section-heading"><h2>ソリューション一覧 <span>{solutions.length} 件</span></h2>
      <button type="button" className="button" disabled={disabled || searching || !root} onClick={() => setRefresh(value => value + 1)}><Icon name="refresh" size={14} />{searching ? "検索中…" : "一覧を更新"}</button></div>
    <p className="solution-hint">{selectionEnabled ? "選択した .sln を1件ずつ処理します。実行する順番は次の確認画面に表示します。" : "対象フォルダ配下を再帰的に検索しています。実行設定で処理する .sln を選択できます。"}</p>
    {selectionEnabled && <div className="solution-selection-actions"><span>{selected.length} 件選択</span><button type="button" className="text-button" disabled={disabled || readOnly || searching || !solutions.length} onClick={() => select(solutions)}>すべて選択</button><button type="button" className="text-button" disabled={disabled || readOnly || !selected.length} onClick={() => select([])}>選択を解除</button></div>}
    {error && <p className="field-validation-error" role="alert">{error}</p>}
    <div className="solution-selection-list">{listed.map(path => <label key={path}>
      {selectionEnabled && <input type="checkbox" disabled={disabled || readOnly} checked={selected.includes(path)} onChange={event => select(event.target.checked ? [...selected, path] : selected.filter(value => value !== path))} />}
      <span>{path}</span>{!searching && !solutions.includes(path) && <small>現在の一覧にありません</small>}
    </label>)}{!listed.length && <p className="solution-hint" role="status">{searching ? ".sln を検索中…" : "対象フォルダ内に .sln が見つかりませんでした。"}</p>}</div>
  </section>;
}

export function SolutionSettings({ config, workspaceId, root, disabled, readOnly, onChange }: { config?: SolutionConfig; workspaceId: string; root: string; disabled: boolean; readOnly?: boolean; onChange: (config: SolutionConfig) => void }) {
  const value = { ...defaultConfig.solution!, ...config };
  const change = <K extends keyof SolutionConfig>(key: K, next: SolutionConfig[K]) => onChange({ ...value, [key]: next });
  const issue = solutionConfigurationIssue(value);
  return <section className="solution-settings" aria-label="ソリューションのビルド設定">
    <SolutionSelection config={value} workspaceId={workspaceId} root={root} disabled={disabled} readOnly={readOnly} onChange={onChange} />
    <div className="solution-section-heading"><h2>共通のビルド設定</h2><span>Visual Studio 2022 / VB.NET</span></div>
    <div className="solution-settings-grid">
      <label className="field"><span className="field-label">ビルド構成</span><input disabled={disabled} value={value.configuration} onChange={event => change("configuration", event.target.value)} placeholder="Debug" /></label>
      <label className="field"><span className="field-label">プラットフォーム</span><input disabled={disabled} value={value.platform} onChange={event => change("platform", event.target.value)} placeholder="Any CPU" /></label>
      <label className="field solution-wide"><span className="field-label">MSBuild の実行ファイル（省略可）</span><input className="code-input" disabled={disabled} value={value.msbuildPath} onChange={event => change("msbuildPath", event.target.value)} placeholder="空欄で Visual Studio 2022 の MSBuild を自動検出" spellCheck={false} />
        <span className="solution-hint">ビルド環境には VS2022 または Build Tools と、対象プロジェクトの SDK・参照アセンブリが必要です。</span>
      </label>
      <label className="field"><span className="field-label">修正回数の上限</span><input type="number" min={0} step={1} disabled={disabled} value={value.maxIterations} onChange={event => change("maxIterations", Number(event.target.value))} />
        <span className="solution-hint">ソリューションごとに、この回数で修正を終了します。0 は回数を制限せず実行します。</span>
      </label>
      <label className="field"><span className="field-label">1 回のビルドの制限時間（秒）</span><input type="number" min={1} max={86400} step={1} disabled={disabled} value={value.buildTimeoutSeconds} onChange={event => change("buildTimeoutSeconds", Number(event.target.value))} /></label>
    </div>
    {issue && <p className="field-validation-error" role="status"><Icon name="info" size={14} />{issue}</p>}
  </section>;
}

export function SolutionReview({ state, onEdit }: { state: State; onEdit: () => void }) {
  const config = { ...defaultConfig.solution!, ...state.config.solution };
  return <section className="solution-review" aria-label="ソリューション実行の確認">
    <div className="solution-section-heading"><h2>ソリューション単位で実行</h2><button type="button" className="text-button" onClick={onEdit}>設定を変更<Icon name="arrow" size={13} /></button></div>
    <dl className="solution-facts"><dt>処理するソリューション</dt><dd><ol className="solution-selected-paths">{selectedSolutionPaths(config).map(path => <li key={path}>{path}</li>)}</ol></dd><dt>ビルド設定</dt><dd>{config.configuration} / {config.platform}</dd><dt>MSBuild</dt><dd>{config.msbuildPath || "Visual Studio 2022 を自動検出"}</dd><dt>修正回数の上限</dt><dd>{config.maxIterations === 0 ? "無制限" : `${config.maxIterations} 回`}</dd><dt>ビルドの制限時間</dt><dd>{config.buildTimeoutSeconds} 秒 / 回</dd><dt>適用ルール</dt><dd>{state.rules.filter(rule => !state.config.excludedRuleIds?.includes(rule.id)).map(rule => rule.title || rule.id).join("、")}</dd></dl>
    <ol className="solution-process"><li>確認した順番に、作業コピーで各ソリューションを初回ビルド</li><li>共通ルールと各ファイルに条件が一致するルール、ビルド結果を基に修正案を作り、別のAI会話で独立レビュー</li><li>レビューに合格した候補を再ビルドし、両方に合格するまで修正を繰り返す</li></ol>
    <p className="solution-hint">原本のフォルダとチェックアウト中のブランチは変更しません。「コミット」では新しいブランチを作成します。要確認になったソリューションは記録して次へ進みます。停止や環境・通信エラーの場合は残りを未実行にします。共有ファイルを修正する場合は、先に成功したソリューションも必要に応じて再検証します。</p>
  </section>;
}

interface SolutionResultsProps {
  state: State; busy: boolean; usable: boolean; canSetup: boolean; onStop: () => void; onExport: (executionId?: string) => void; onOpenWorktree: () => void; onSetup: () => void;
}

export function SolutionExecutionResultsPanel(props: SolutionResultsProps) {
  const [selection, setSelection] = useState("");
  const [request, setRequest] = useState(0);
  const runs = props.state.executionRuns;
  const latest = runs.at(-1);
  const selected = runs.find(run => run.id === selection);
  const historical = Boolean(selected && selected.id !== latest?.id);
  const result = useResultSnapshot<ExecutionRunResult>({ key: `${props.state.activeWorkspaceId}:${selected?.id || ""}`, revision: String(request), enabled: historical, poll: false, read: () => api.GetExecutionRun(selected!.id) });
  return <section className="execution-results" aria-label="ソリューションの実行履歴と結果">
    {latest && <div className="execution-run-selector"><label htmlFor="solution-execution-run"><Icon name="clock" size={15} />実行履歴</label><select id="solution-execution-run" value={selected ? selection : ""} onChange={event => setSelection(event.target.value)}>
      <option value="">最新の実行 · {executionLabel(latest)}</option>
      {[...runs].reverse().map(run => <option key={run.id} value={run.id}>{executionLabel(run)}</option>)}
    </select></div>}
    {historical && props.state.running && <p className="solution-hint" role="status">新しい実行が進行中です。<button type="button" className="text-button" onClick={() => setSelection("")}>最新の実行を表示</button></p>}
    {historical && result.error && <p className="field-validation-error" role="alert">{result.error}<button type="button" className="text-button" onClick={() => setRequest(value => value + 1)}>再読み込み</button></p>}
    {!historical ? <SolutionResultsPanel {...props} /> : result.data ? <SolutionResultsPanel {...props} key={selected!.id} state={result.data.state} executionId={selected!.id} /> : !result.error && <p className="solution-hint" role="status">実行結果を読み込み中…</p>}
  </section>;
}

export function SolutionResultsPanel({ state, busy, usable, canSetup, executionId, onStop, onExport, onOpenWorktree, onSetup }: SolutionResultsProps & { executionId?: string }) {
  const [pathChoice, setPathChoice] = useState("");
  const batch = state.solutionBatch;
  const selectedPath = batch?.paths.includes(pathChoice) ? pathChoice : batch?.paths[batch.currentIndex] || batch?.paths[0] || "";
  const run = batch ? batch.runs.find(item => item.path === selectedPath) : state.solutionRun;
  const liveRunning = state.running && !executionId;
  const running = liveRunning && run?.status === "running";
  const [buildChoice, setBuildChoice] = useState<number | null>(null);
  const [file, setFile] = useState("");
  const [view, setView] = useState<"diff" | "before" | "after">("diff");
  const [detail, setDetail] = useState<FileDetail | null>(null);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  const files = run?.status === "completed" ? run.files : [];
  const selectedFile = files.includes(file) ? file : files[0] || "";
  const builds = run?.builds || [];
  const selectedBuild = builds.find(build => build.iteration === buildChoice) || builds.at(-1);
  useEffect(() => { setBuildChoice(null); setFile(""); }, [run?.id]);
  useEffect(() => {
    let current = true;
    setDetail(null); setError("");
    if (selectedFile && run && !running) {
      const attemptIndex = solutionAttemptIndex(state.tasks, selectedFile, run);
      if (attemptIndex === null) setError("このソリューションの差分履歴が見つかりません。結果の記録を確認してください。");
      else (executionId ? api.GetExecutionFileDetail(executionId, selectedFile, attemptIndex) : api.GetFileDetail(selectedFile, attemptIndex)).then(value => { if (current) setDetail(value); }).catch(failure => { if (current) setError(message(failure)); });
    }
    return () => { current = false; };
  }, [state.activeWorkspaceId, run?.id, run?.finishedAt, selectedFile, running, executionId, retry]);
  return <section className="solution-results" aria-label="ソリューションの実行結果">
    {batch && <section className="solution-batch-summary" aria-label="選択したソリューションの処理状況"><div className="solution-section-heading"><h2>{solutionStatusLabel(batch.status)}</h2><span>{batch.runs.filter(item => item.status === "completed").length} / {batch.paths.length} 件成功</span></div>
      <label className="field"><span className="field-label">結果を表示するソリューション</span><select value={selectedPath} onChange={event => setPathChoice(event.target.value)}>{batch.paths.map((path, index) => {
        const item = batch.runs.find(value => value.path === path);
        return <option key={path} value={path}>{index + 1}. {path} · {solutionStatusLabel(item?.status || (batch.status === "running" ? "pending" : "not_run"))}</option>;
      })}</select></label>{batch.note && <p className="solution-hint">{batch.note}</p>}
    </section>}
    <div className="solution-section-heading"><div><h2>{run ? solutionStatusLabel(run.status) : "ソリューションの実行結果"}</h2>{run && <p className="solution-hint">修正 {run.iteration} 回 · ビルド {builds.length} 回 · {run.status === "completed" ? `変更 ${files.length}` : `対象 ${run.files.length}`} ファイル</p>}</div>
      <div className="solution-actions">{liveRunning ? <button type="button" className="button" disabled={busy || !usable} onClick={onStop}><Icon name="stop" size={14} />停止</button> : <button type="button" className="button" disabled={busy || !canSetup} onClick={onSetup}>実行設定へ</button>}
        <button type="button" className="button" disabled={(!run && !batch) || busy || !usable} onClick={() => onExport(executionId)}>結果を出力</button>
        {!executionId && <button type="button" className="button" disabled={!state.worktree || busy || !usable} onClick={onOpenWorktree}>作業コピーを開く</button>}</div>
    </div>
    {run?.note && <p className="solution-run-note" role="status">{run.note}</p>}
    {run && <div className="solution-usage" aria-label="この実行のAI使用量">
      <span>入力 {quantity(run.usage.inputTokens)} / キャッシュ {quantity(run.usage.cachedTokens)} / 出力 {quantity(run.usage.outputTokens)} tokens</span>
      <span>推定 ${Number(run.usage.costUsd || 0).toFixed(6)} · {quantity(run.usage.turns)} 要求</span>
      {(run.usage.uncertain || run.requestPending) && <span role="status">{run.requestPending ? "AI応答待ち · 使用量は受信後に更新" : "一部の使用量は未計上"}</span>}
    </div>}
    {running && <p className="solution-hint" role="status"><span className="spinner" /> ビルドと修正を実行しています。結果は自動更新されます。</p>}
    {!run ? <div className="results-empty"><Icon name="terminal" size={30} /><p>{batch ? "このソリューションはまだ実行されていません。" : "実行すると、初回ビルドから修正後までの結果が表示されます。"}</p></div> : <>
      <section className="solution-build-results" aria-label="ビルド履歴">
        <label className="field"><span className="field-label">ビルド履歴</span><select value={buildChoice ?? ""} onChange={event => setBuildChoice(event.target.value === "" ? null : Number(event.target.value))} disabled={!builds.length}>
          <option value="">最新のビルド{selectedBuild ? ` · ${builds.at(-1)?.success ? "成功" : "失敗"}` : "を待機中"}</option>
          {[...builds].reverse().map(build => <option key={build.iteration} value={build.iteration}>{build.iteration === 0 ? "初回ビルド" : `修正 ${build.iteration} 回目のビルド`} · {build.success ? "成功" : "失敗"} · エラー {build.diagnostics.length} 件</option>)}
        </select></label>
        {selectedBuild && <>
          <div className="solution-build-status"><strong>{selectedBuild.iteration === 0 ? "初回ビルド" : `修正 ${selectedBuild.iteration} 回目`}</strong><span>{selectedBuild.success ? "ビルド成功" : `ビルド失敗（終了コード ${selectedBuild.exitCode}）`}</span></div>
          {selectedBuild.diagnostics.length > 0 && <div className="solution-diagnostics"><table><thead><tr><th>コード</th><th>ファイル / 行</th><th>コンパイルエラー</th></tr></thead><tbody>{selectedBuild.diagnostics.map((diagnostic, index) => <tr key={index}><td>{diagnostic.code || "—"}</td><td>{diagnostic.file || "ソリューション"}{diagnostic.line ? `:${diagnostic.line}` : ""}</td><td>{diagnostic.message}</td></tr>)}</tbody></table></div>}
          <details className="solution-build-output"><summary>ビルドログを表示</summary><pre>{selectedBuild.output || "ビルド出力はありません。"}</pre></details>
        </>}
      </section>
      <section className="solution-review-results" aria-label="ソリューションの独立レビュー">
        <h3>独立レビュー <span>{(run.reviews || []).length} 件</span></h3>
        {(run.reviews || []).map((review, index) => <details className="solution-independent-review" key={`${review.iteration}:${index}`}>
          <summary><span>修正 {review.iteration + 1} 回目 · レビュー {index + 1}</span><strong>{reviewVerdict(review.verdict)}</strong></summary>
          {review.summary && <p>{review.summary}</p>}
          {review.assessments.length > 0 && <dl className="solution-review-assessments">{review.assessments.map((assessment, assessmentIndex) => <div key={`${assessment.ruleId}:${assessmentIndex}`}><dt><code>{assessment.ruleId}</code><span>{assessmentStatus(assessment.status)}</span></dt><dd>{assessment.reason || "理由の記録はありません。"}</dd></div>)}</dl>}
          <p className="solution-hint">入力 {quantity(review.usage.inputTokens)} / 出力 {quantity(review.usage.outputTokens)} tokens · 推定 ${Number(review.usage.costUsd || 0).toFixed(6)}{review.usage.uncertain ? " · 一部未計上" : ""}</p>
        </details>)}
        {!(run.reviews || []).length && <p className="solution-hint">{running ? "候補の独立レビューが完了すると、判定とルールごとの評価を表示します。" : "独立レビューの記録はありません。"}</p>}
      </section>
      <SolutionTracePanel workspaceId={state.activeWorkspaceId} executionId={executionId || run.executionId || batch?.id || run.id} solutionRunId={run.id} running={running} />
      <section className="solution-file-results" aria-label="ソリューションの変更差分"><h3>変更ファイル</h3>
        {files.length ? <><label className="field"><span className="field-label">ファイル</span><select value={selectedFile} onChange={event => setFile(event.target.value)}>{files.map(path => <option key={path} value={path}>{path}</option>)}</select></label>
          <div className="solution-code-tabs" role="tablist" aria-label="ファイルの表示">{(["diff", "before", "after"] as const).map(tab => <button key={tab} type="button" role="tab" aria-selected={tab === view} onClick={() => setView(tab)}>{tab === "diff" ? "差分" : tab === "before" ? "変更前" : "変更後"}</button>)}</div>
          {running ? <p className="solution-hint">変更差分は実行が終了すると表示されます。</p> : error ? <p className="field-validation-error" role="alert">{error}<button type="button" className="text-button" onClick={() => setRetry(value => value + 1)}>再読み込み</button></p> : detail ? <ResultCode content={view === "diff" ? detail.diff : view === "before" ? detail.before : detail.after} view={view} isDiff={view === "diff"} /> : <p className="solution-hint" role="status">差分を読み込み中…</p>}
        </> : <p className="solution-hint">{running ? "変更差分は独立レビューとビルドの成功後に表示されます。" : run.status !== "completed" ? "今回の実行は完了していません。ビルドログと終了理由を確認してください。" : "変更されたファイルはありません。"}</p>}
      </section>
    </>}
  </section>;
}
