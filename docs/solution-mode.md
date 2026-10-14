# VB.NET ソリューションモード

既存のワークスペースに **VB.NET ソリューション単位（VS2022）** の処理モードを追加しています。LLM接続、ルール編集、実行履歴、結果出力、Git作業コピー、コミット画面は同じアプリを使います。

対象フォルダ配下の `.sln` を再帰的に一覧にし、選択したソリューションを1件ずつ順番に処理します。各ソリューションを最初にビルドし、その結果と共通・条件付きルールをAIへ渡します。AIはVB.NETソースと関連するXAML・リソース・設定ファイルの修正案を作り、別のAI会話でソリューション全体の候補を独立レビューします。レビューに合格した候補を検証用コピーへ適用して再ビルドし、独立レビューとビルドの両方に合格した修正一式だけを採用します。

## 利用に必要なもの

- Windows と Visual Studio 2022 または Build Tools 2022 の MSBuild。
- 対象のVB.NETプロジェクトに必要な .NET デスクトップ開発ツール、SDK、.NET Frameworkのターゲットパック、参照アセンブリ。
- NuGet復元に必要な接続・認証設定。各ビルドでパッケージ復元も行います。
- `.sln`、参照する `.vbproj` とVBソースを含む、初回コミット済みで未コミット変更のないGitリポジトリ。
- 登録済みのLLM接続と、既存と同じ形式のルール。

MSBuildのパスが空欄なら、Visual Studio Installerの `vswhere.exe` からVS2022を検索します。自動検出できない場合は、VS2022に含まれる `MSBuild.exe` の絶対パスを指定してください。対象プロジェクトのビルドはWindows専用です。macOSではアプリの起動や設定・結果の閲覧はできますが、VS2022でのビルド実行はできません。

## GUIでの操作

1. 対象リポジトリのワークスペースを作成します。
2. 「対象フォルダ」で処理モードを「VB.NET ソリューション単位（VS2022）」にします。
3. 「ルール」で通常の共通ルール・条件付きルールを作成するか、JSON・CSVを取り込みます。従来の3項目JSON・`SKILL.md`・スキルフォルダも互換形式として取り込めます。
4. 「実行設定」でLLM接続と適用ルールを選び、自動検索した一覧から処理する `.sln` を複数選択します。最初は全件を選択します。「すべて選択」「選択を解除」でまとめて変更できます。
5. ビルド構成、プラットフォーム、修正回数の上限、ビルドの制限時間を確認します。
6. 「確認」で処理順を確認して実行します。「結果確認」で各ソリューションを切り替え、ビルド履歴・コンパイルエラー・ログ・独立レビューの判定とルール別評価・使用量・そのソリューションが採用した差分を確認します。
7. 成功した結果を「コミット」から新しいブランチの1コミットにまとめます。

実行前はモードを変更できます。作業コピーや実行履歴があるワークスペースはモードを固定します。別モードで処理する場合は新しいワークスペースを作成してください。ソリューションの選択は実行後も変更できます。ルール形式とルール編集画面はファイル単位モードと共用します。

ソリューションモードにファイルの選択・除外・並列数の設定はありません。選択した各 `.sln` と参照するVB.NETプロジェクトを処理単位とし、一覧のパス順に逐次処理します。過去に取り込んだ診断JSONLも使用せず、今回のビルドで得たエラーを使用します。

| 設定 | 意味 | 既定値 |
| --- | --- | --- |
| `solution.paths` | 対象フォルダからの `.sln` 相対パスの配列 | 未指定なら初回検索で全件選択 |
| `solution.path` | 旧設定の単一パス。新規設定では空文字 | `paths` が未指定の場合だけ使用 |
| `solution.msbuildPath` | VS2022の `MSBuild.exe` の絶対パス | 空欄で自動検出 |
| `solution.configuration` | ソリューションのビルド構成 | `Debug` |
| `solution.platform` | ソリューションのプラットフォーム | `Any CPU` |
| `solution.maxIterations` | 各ソリューションの修正回数の上限。`0` は回数を制限しない | `20` |
| `solution.buildTimeoutSeconds` | ビルド1回の制限時間。1〜86400秒 | `300` |

`paths: []` は全解除であり、実行できません。`paths` が未指定または `null` で旧 `path` も空なら、対象フォルダを検索して全件を初期選択します。旧 `path` があればその1件を維持します。保存済みの選択は一覧を更新しても勝手に増減しません。GUIで選択を変更するとパス順で保存します。CLIで指定した配列の順番は、GUIで選択を変更するまで保持し、確認画面にもその順番で表示します。

## 共通ルールと条件付きルール

ファイル単位モードと同じルールを共用します。各ルールはID、名称、説明、対象ファイルのglob、内容の正規表現、診断の正規表現、Markdown本文を持ちます。3つの条件がすべて空欄なら共通ルールです。条件がある場合は、ソリューションの関連ファイルに対し、指定したすべての条件を満たすファイルへ適用します。診断条件には今回のビルドで得たエラーを使います。

対象ファイルのglobと内容条件の正規表現は既定で大文字・小文字を区別しません。本文条件だけ区別する場合は先頭に `(?-i)` を付けます。診断条件は既定で区別します。共通ルールも、実行設定で選択を解除すれば適用しません。同じ一括実行で先行するソリューションが共有ファイルへ適用したルールは後続にも引き継ぎます。先行修正で内容条件の文字列がなくなっても、そのルールを維持して後続の変更を確認します。

通常のJSONパッケージは、YAML front matter付きMarkdown文字列の配列です。

```json
{
  "rules": [
    "---\nid: preserve-behavior\nname: 既存動作の維持\ndescription: 公開APIの振る舞いを維持する。\npath_pattern: ''\ncontent_pattern: ''\ndiagnostic_pattern: ''\n---\n# 修正方針\n公開APIの振る舞いと例外処理を維持する。",
    "---\nid: fix-vb-types\nname: VBの型変換を修正\ndescription: 型の契約を確認してコンパイルエラーを修正する。\npath_pattern: '**/*.vb'\ncontent_pattern: ''\ndiagnostic_pattern: 'BC30512|BC late binding'\n---\n# 修正方針\n暗黙の型変換を明示的にし、関連する宣言と呼び出し元を確認する。"
  ]
}
```

CSVの必須ヘッダーは `id,name,description,path_pattern,content_pattern`、`diagnostic_pattern` は任意です。その他の列は列名をMarkdown見出し、セルを本文にします。「Excel用テンプレート（CSV）」から雛形を出力できます。IDは64文字以内で、英数字で始まる英数字・ハイフン・アンダースコアです。

旧ソリューションルールの3項目形式は、名称をID、条件を空欄とした共通ルールへ変換して読み込みます。通常の編集・保存・JSON書き出しは上記の共用形式になります。

```json
{
  "rules": [
    {
      "id": "vbnet-option-strict",
      "description": "Option Strict On を維持して型変換を修正する。",
      "content": "# 修正方針\n型の契約を確認し、宣言と呼び出し元を必要に応じて修正する。"
    }
  ]
}
```

同名の3列 `id,description,content` のCSVも互換入力として取り込めます。

## Skillsとの対応

「スキルとして書き出す」は、指定したディレクトリに次の構造を作ります。

```text
skills/
  vbnet-option-strict/
    SKILL.md
```

`SKILL.md` の中身は、そのままスキルとして利用できる構造です。

```markdown
---
name: vbnet-option-strict
description: Option Strict On に対応するため、暗黙の型変換と遅延バインディングを修正する。
---
# 修正方針

Option Strict On を維持し、コンパイルエラーの原因を修正する。
型の契約を確認して明示的な変換を使う。宣言と呼び出し元の両方を必要に応じて変更する。
```

| ルール項目 | スキル内の対応 |
| --- | --- |
| `id` | フォルダ名とfront matterの `name` |
| `description` | front matterの `description` |
| Markdown本文（旧 `content`） | front matterより後のMarkdown本文 |

単一の `SKILL.md`、それを含むスキルフォルダ、複数のスキルフォルダが並ぶ親ディレクトリを取り込めます。フォルダ名と `name` は一致させます。front matterは `name`・`description` の2項目だけで、追加資材の `scripts/`・`references/` 等は取り込みません。ルールの指示は本文内で完結させてください。

スキルから取り込んだルールは条件なしの共通ルールになります。スキル形式には抽出条件がないため、条件付きルールの書き出しは拒否します。書き出すIDは64文字以内の英小文字・数字・ハイフンとし、先頭・末尾・連続するハイフンやWindowsの予約名は使えません。説明は1〜1024文字です。

マージ時に同じIDがある場合は連番を付けて追加します。書き出し先に同名のスキルフォルダがある場合は上書きせず、別の保存先を選ぶ必要があります。

## ビルドと修正の流れ

```text
選択したソリューションを設定の順番に処理
  → 採用済み作業コピーから検証用コピーを作成
  → 初回のソリューションビルド（iteration 0）
  → ソリューション・プロジェクト・VBソース・関連テキスト・ルール・ビルド結果をAIへ渡す
  → 関連する複数ファイルの修正案を作成
  → 別のAI会話で同じソリューション候補を独立レビュー
  → 指摘があれば候補を修正し、合格するまで再レビュー
  → レビュー合格候補だけを検証用コピーに適用
  → ソリューションを再ビルド（共有ファイル変更時は先に成功したものも必要に応じて再検証）
  → 失敗なら新しいエラーと修正後のコードで再度修正・独立レビュー
  → 独立レビューとビルドに合格した一式を採用済み作業コピーへ1コミットで保存
  → 次の選択ソリューションへ進む
```

初回ビルドが成功しても、ルールに沿った修正が必要かをAIに確認します。変更不要の候補も独立レビューし、合格した結果を記録します。修正案がある場合は、初回が成功していても、同じ候補の独立レビューと修正後の再ビルドに合格するまで採用しません。

MSBuildには固定の `/t:Rebuild`・`/restore` と指定した構成・プラットフォームを渡します。AIがビルドコマンドを自由に作る方式ではありません。ログ、終了コード、コンパイラのエラーコード・メッセージ・ファイル・行を各ビルドに記録します。採用にはソリューション候補の独立レビュー合格とビルド成功の両方が必要です。レビューは判定・説明・ルール別評価・元データと候補のハッシュ・使用量を記録し、結果画面で反復ごとの評価を確認できます。ビルド成功はコンパイルが通ることを示します。独立レビューもLLMによる判定であり、アプリケーションの動作テストは別途必要です。

回数を無制限にしても、人の判断が必要な状態、同じ修正の繰り返し、修正案の欠落では、そのソリューションを要確認として終了し次へ進みます。利用者の停止や認証・ビルド環境・通信・保存・整合性の問題では一括実行を停止し、残りを未実行として記録します。制限時間はビルド1回ごとで、実行全体の時間ではありません。

| 結果 | 意味 |
| --- | --- |
| `completed` | 同じソリューション候補の独立レビューとビルドの両方に合格し、結果を採用した |
| `needs_human` | 上限到達、修正の繰り返し、仕様確認等でそのソリューションの自動修正を終了し、次へ進む |
| `failed` | ビルド環境・通信・保存・整合性等のエラーで一括実行を終了した |
| `stopped` | 利用者が一括実行を停止した |
| `not_run` | 先行するエラー・停止により実行しなかった |

上限到達や停止時の未合格の修正案は、採用済みの作業コピーへ反映しません。ログ・終了理由・使用量は保持します。先に成功したソリューションの変更は保持します。次に実行すると、最後に採用済みの内容から選択した各ソリューションの初回ビルドをやり直します。結果の差分はソリューションの実行IDに対応する履歴を表示するため、共有ファイルへの後続の変更と混ざりません。ファイル単位での再試行・変更破棄は、ソリューション全体のビルド結果を無効にするため使用できません。

## CLIでの実行

Windows PowerShellの例です。最初に以下を `solution-settings.json` として保存します。

```json
{
  "mode": "solution",
  "solution": {
    "paths": ["src/AppA.sln", "src/AppB.sln"],
    "path": "",
    "configuration": "Debug",
    "platform": "Any CPU",
    "msbuildPath": "",
    "maxIterations": 20,
    "buildTimeoutSeconds": 300
  }
}
```

```powershell
./onebyone-cli.exe workspace create --name "VB.NET 移行" --root C:/src/LegacyApplication
./onebyone-cli.exe settings update --input ./solution-settings.json
./onebyone-cli.exe rules import --input ./solution-rules.json --mode replace
./onebyone-cli.exe llm select --id <接続ID>
./onebyone-cli.exe run --json
./onebyone-cli.exe runs list --json
./onebyone-cli.exe report --output ./solution-result.json
```

LLM接続が未登録なら先に [CLIの接続登録](cli.md#llm接続を登録する)を行います。ソリューションモードでは `scan`・`selection` は実行の前提ではありません。`run --limit`・`run --concurrency` によるファイル単位の制限は使用せず、`solution.maxIterations` を使ってください。

スキルとの入出力や、修正回数だけの部分更新もできます。

```powershell
./onebyone-cli.exe rules import --input ./skills --mode merge
./onebyone-cli.exe rules export-skills --output ./exported-skills
./onebyone-cli.exe rules export --output ./solution-rules.json
'{"solution":{"maxIterations":0}}' | ./onebyone-cli.exe settings update --input -
```

`rules create` / `rules update` は既存モードと同じ `id,name,description,pathPattern,contentPattern,diagnosticPattern,body` を使います。更新には `rules show --id ID --json` の `revision` を `expectedRevision` として加えます。旧クライアントの `id,description,content` 入力も互換変換します。`settings update` は入れ子の `solution` についても指定した項目だけを変更し、未指定項目と `maxIterations: 0` を保持します。

終了コードは成功 `0`、操作・設定・開始前のエラー `1`、一括実行に `failed` / `needs_human` がある場合 `2`、停止 `130` です。Ctrl+Cは子プロセスの停止と状態保存を待ちます。

## 対象範囲と検証状況

Visual Studio形式の `.sln` に含まれるVB.NETプロジェクトと、そのVB.NETの `ProjectReference` をたどります。従来形式の `Compile` だけでなく、`Page`・`ApplicationDefinition`・`EmbeddedResource`・`Resource`・`Content`・`None`・`AdditionalFiles`・`PRIResource` の入力から、VBソース、XAML、resx、config、XML等の対応するテキストを処理します。SDK形式の既定項目も対象です。Gitで管理された `Designer.vb` や自動生成属性付きのソースも含め、`bin` / `obj` などの出力フォルダは編集しません。画像、アセンブリ、署名キー等のバイナリは編集対象外です。

物理ファイルは `.vbproj` の場所を基準にした `Include` から解決します。`Link` はVisual Studio内での表示パス、`DependentUpon` は親子表示のメタデータであり、読み取り元を指定しません。例えば次の項目では、実際のXAMLは `../Shared/Views/Dialog.xaml`、コードは `../Shared/Views/Dialog.xaml.vb` です。画面上の `LinkedViews/Dialog.xaml` という別ファイルを探すことはありません。

```xml
<ItemGroup>
  <Page Include="..\Shared\Views\Dialog.xaml">
    <Link>LinkedViews\Dialog.xaml</Link>
  </Page>
  <Compile Include="..\Shared\Views\Dialog.xaml.vb">
    <Link>LinkedViews\Dialog.xaml.vb</Link>
    <DependentUpon>Dialog.xaml</DependentUpon>
  </Compile>
  <EmbeddedResource Include="..\Shared\Resources.resx">
    <Link>My Project\Resources.resx</Link>
  </EmbeddedResource>
  <None Include="App.config" />
</ItemGroup>
```

リンク先も対象フォルダの中に収まり、Gitで管理されている必要があります。対象フォルダ外への参照やシンボリックリンクは拒否します。共有ファイルは物理パスで同一と判断し、同じ候補内に重複して載せません。

入力集合は静的に調べるため、独自のMSBuildプロパティ式で組み立てる項目パスや任意のインポートによる動的な入力集合には対応していません。C#など他言語のプロジェクトを含む場合もビルドはソリューション全体ですが、編集対象はVB.NETプロジェクトの対応する入力、`.vbproj`、`.sln` です。新しいファイルの追加・削除、対象外のコード・認証関連ファイルの編集には対応していません。プロジェクト変更で参照プロジェクトや入力ファイルの集合が変わる候補は、ビルド前に拒否します。

Gitの `eol` 属性は扱いますが、独自の `filter` や `working-tree-encoding` 属性を持つ入力は、検証用コピーと採用結果の内容を同一に保つため拒否します。

既存のファイル単位モードと同様、原本のチェックアウトは書き換えません。ビルドに失敗した候補は検証用コピーにとどまり、終了時にそのコピーを片付けます。成功後も原本ブランチへのマージやリモートへのプッシュは自動で行いません。

今回の開発環境では、モックのLLM・ビルド処理、使い捨てGitリポジトリ、MSBuildの引数・診断解析、停止処理、画面・CLIを自動テストしています。Windows実機上のVS2022による実ビルドと、実際のLLMを組み合わせた実ソリューションの移行は未検証です。対象環境でビルド構成と必要なSDKを確認してから利用してください。
