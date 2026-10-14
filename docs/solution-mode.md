# VB.NET ソリューションモード

既存のワークスペースに **VB.NET ソリューション単位（VS2022）** の処理モードを追加しています。LLM接続、ルール編集、実行履歴、結果出力、Git作業コピー、コミット画面は同じアプリを使います。

最初に `.sln` 全体をビルドし、その結果とルールをAIへ渡します。AIは関連する複数のVB.NETファイルの修正案を作り、別のAI会話でソリューション全体の候補を独立レビューします。レビューに合格した候補を検証用コピーへ適用して再ビルドし、独立レビューとビルドの両方に合格した修正一式だけを採用します。

## 利用に必要なもの

- Windows と Visual Studio 2022 または Build Tools 2022 の MSBuild。
- 対象のVB.NETプロジェクトに必要な .NET デスクトップ開発ツール、SDK、.NET Frameworkのターゲットパック、参照アセンブリ。
- NuGet復元に必要な接続・認証設定。各ビルドでパッケージ復元も行います。
- `.sln`、参照する `.vbproj` とVBソースを含む、初回コミット済みで未コミット変更のないGitリポジトリ。
- 登録済みのLLM接続と、ソリューション用のルール。

MSBuildのパスが空欄なら、Visual Studio Installerの `vswhere.exe` からVS2022を検索します。自動検出できない場合は、VS2022に含まれる `MSBuild.exe` の絶対パスを指定してください。対象プロジェクトのビルドはWindows専用です。macOSではアプリの起動や設定・結果の閲覧はできますが、VS2022でのビルド実行はできません。

## GUIでの操作

1. 対象リポジトリのワークスペースを作成します。
2. 「対象フォルダ」で処理モードを「VB.NET ソリューション単位（VS2022）」にします。
3. 「ルール」でソリューション用ルールを作成するか、JSON・CSV・`SKILL.md`・スキルフォルダを取り込みます。
4. 「実行設定」でLLM接続と適用ルールを選び、`.sln` を指定します。「.sln を検索」から対象フォルダ内の候補を選べます。
5. ビルド構成、プラットフォーム、修正回数の上限、ビルドの制限時間を確認します。
6. 「確認」から実行し、「結果確認」でビルド履歴・コンパイルエラー・ログ・独立レビューの判定とルール別評価・使用量・採用された差分を確認します。
7. 成功した結果を「コミット」から新しいブランチの1コミットにまとめます。

実行前はモードを変更できます。作業コピーや実行履歴があるワークスペースはモードを固定します。別モードで処理する場合は新しいワークスペースを作成してください。ファイル単位モードのルールには名称や抽出条件があり、ソリューション用の3項目のルールとは形式が異なります。

ソリューションモードにファイルの選択・除外・並列数の設定はありません。対象 `.sln` と参照するVB.NETプロジェクトを処理単位とします。過去に取り込んだ診断JSONLも使用せず、今回のビルドで得たエラーを使用します。

| 設定 | 意味 | 既定値 |
| --- | --- | --- |
| `solution.path` | 対象フォルダからの `.sln` 相対パス | 指定が必要 |
| `solution.msbuildPath` | VS2022の `MSBuild.exe` の絶対パス | 空欄で自動検出 |
| `solution.configuration` | ソリューションのビルド構成 | `Debug` |
| `solution.platform` | ソリューションのプラットフォーム | `Any CPU` |
| `solution.maxIterations` | 修正を試みる回数の上限。`0` は回数を制限しない | `20` |
| `solution.buildTimeoutSeconds` | ビルド1回の制限時間。1〜86400秒 | `300` |

## ルールは3項目

ルール定義は `id`・`description`・`content` だけです。名称、ファイル条件、内容の正規表現、診断条件はありません。修正対象や条件はMarkdownの `content` に記載します。

```json
{
  "rules": [
    {
      "id": "vbnet-option-strict",
      "description": "Option Strict On に対応するため、暗黙の型変換と遅延バインディングを修正する。",
      "content": "# 修正方針\n\nOption Strict On を維持し、コンパイルエラーの原因を修正する。\n型の契約を確認して明示的な変換を使う。宣言と呼び出し元の両方を必要に応じて変更する。\n\n# 維持すること\n\n公開APIの振る舞いと例外処理を維持する。型の契約を決められない場合は理由を示して確認を求める。"
    }
  ]
}
```

IDは64文字以内の英小文字・数字・ハイフンです。先頭・末尾・連続するハイフンとWindowsの予約名は使えません。説明は1〜1024文字、内容は空ではないMarkdownです。IDは作成時に入力し、保存後は変更しません。

JSONの最上位は `rules`、各ルールは上記3項目だけを受け付けます。ファイル単位モードのJSON文字列配列や抽出条件を混在させないでください。3列 `id,description,content` のCSVもGUI・CLIから取り込めます。

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
| `content` | front matterより後のMarkdown本文 |

単一の `SKILL.md`、それを含むスキルフォルダ、複数のスキルフォルダが並ぶ親ディレクトリを取り込めます。フォルダ名と `name` は一致させます。front matterは `name`・`description` の2項目だけで、追加資材の `scripts/`・`references/` 等は取り込みません。ルールの指示は本文内で完結させてください。

マージ時に同じIDがある場合は `-2`、`-3` のような末尾を付けて追加します。書き出し先に同名のスキルフォルダがある場合は上書きせず、別の保存先を選ぶ必要があります。

## ビルドと修正の流れ

```text
採用済み作業コピーから検証用コピーを作成
  → 初回のソリューションビルド（iteration 0）
  → ソリューション・プロジェクト・VBソース・ルール・ビルド結果をAIへ渡す
  → 関連する複数ファイルの修正案を作成
  → 別のAI会話で同じソリューション候補を独立レビュー
  → 指摘があれば候補を修正し、合格するまで再レビュー
  → レビュー合格候補だけを検証用コピーに適用
  → ソリューションを再ビルド
  → 失敗なら新しいエラーと修正後のコードで再度修正・独立レビュー
  → 独立レビューとビルドに合格した一式を採用済み作業コピーへ1コミットで保存
```

初回ビルドが成功しても、ルールに沿った修正が必要かをAIに確認します。変更不要の候補も独立レビューし、合格した結果を記録します。修正案がある場合は、初回が成功していても、同じ候補の独立レビューと修正後の再ビルドに合格するまで採用しません。

MSBuildには固定の `/t:Rebuild`・`/restore` と指定した構成・プラットフォームを渡します。AIがビルドコマンドを自由に作る方式ではありません。ログ、終了コード、コンパイラのエラーコード・メッセージ・ファイル・行を各ビルドに記録します。採用にはソリューション候補の独立レビュー合格とビルド成功の両方が必要です。レビューは判定・説明・ルール別評価・元データと候補のハッシュ・使用量を記録し、結果画面で反復ごとの評価を確認できます。ビルド成功はコンパイルが通ることを示します。独立レビューもLLMによる判定であり、アプリケーションの動作テストは別途必要です。

回数を無制限にしても、利用者の停止、人の判断が必要な状態、同じ修正の繰り返し、修正案の欠落、認証・ビルド環境等の問題では終了理由を残して停止します。制限時間はビルド1回ごとで、実行全体の時間ではありません。

| 結果 | 意味 |
| --- | --- |
| `completed` | 同じソリューション候補の独立レビューとビルドの両方に合格し、結果を採用した |
| `needs_human` | 上限到達、修正の繰り返し、仕様確認等により自動修正を終了した |
| `failed` | ビルド環境・通信・保存・整合性等のエラーで終了した |
| `stopped` | 利用者が停止した |

上限到達や停止時の未合格の修正案は、採用済みの作業コピーへ反映しません。ログ・終了理由・使用量は保持します。次に実行すると、最後に採用済みの内容から初回ビルドをやり直します。ファイル単位での再試行・変更破棄は、ソリューション全体のビルド結果を無効にするため使用できません。

## CLIでの実行

Windows PowerShellの例です。最初に以下を `solution-settings.json` として保存します。

```json
{
  "mode": "solution",
  "solution": {
    "path": "src/LegacyApplication.sln",
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

`rules create` は単一の `{ "id": "...", "description": "...", "content": "..." }` を入力します。`rules update` も同じ3項目を使い、更新制御用の `expectedRevision` に `rules show --id ID --json` の `revision` を加えます。`settings update` は入れ子の `solution` についても指定した項目だけを変更し、未指定項目と `maxIterations: 0` を保持します。

終了コードは成功 `0`、操作・設定・開始前のエラー `1`、今回のソリューションが `failed` / `needs_human` で終了した場合 `2`、停止 `130` です。Ctrl+Cは子プロセスの停止と状態保存を待ちます。

## 対象範囲と検証状況

Visual Studio形式の `.sln` に含まれるVB.NETプロジェクトと、そのVB.NETの `ProjectReference` をたどります。従来形式の `Compile Include` とSDK形式の既定VBソースを読み取ります。Gitで管理された `Designer.vb` や自動生成属性付きのソースも含め、`bin` / `obj` などの出力フォルダは編集しません。対象フォルダ外への参照やシンボリックリンクは拒否します。対象フォルダ内のリンクされたVBソースは扱えます。

入力集合は静的に調べるため、独自のMSBuildプロパティ式で組み立てる `Compile` / `ProjectReference` パスや任意のインポートによる動的な入力集合には対応していません。C#など他言語のプロジェクトを含む場合もビルドはソリューション全体ですが、編集対象はVB.NETの入力、`.vbproj`、`.sln` です。新しいファイルの追加・削除、対象外のコード・認証関連ファイルの編集には対応していません。プロジェクト変更で参照プロジェクトやソースの集合が変わる候補は、ビルド前に拒否します。

Gitの `eol` 属性は扱いますが、独自の `filter` や `working-tree-encoding` 属性を持つ入力は、検証用コピーと採用結果の内容を同一に保つため拒否します。

既存のファイル単位モードと同様、原本のチェックアウトは書き換えません。ビルドに失敗した候補は検証用コピーにとどまり、終了時にそのコピーを片付けます。成功後も原本ブランチへのマージやリモートへのプッシュは自動で行いません。

今回の開発環境では、モックのLLM・ビルド処理、使い捨てGitリポジトリ、MSBuildの引数・診断解析、停止処理、画面・CLIを自動テストしています。Windows実機上のVS2022による実ビルドと、実際のLLMを組み合わせた実ソリューションの移行は未検証です。対象環境でビルド構成と必要なSDKを確認してから利用してください。
