# CLIの使い方

CLIはデスクトップアプリと同じ処理・保存先・検証を使います。GUIを先に開かなくても、ワークスペース作成、LLM接続、ルールの取り込み、実行、結果確認、新規ブランチへの結果反映まで行えます。

以下の例は、配布フォルダでmacOSの `./onebyone` を使います。Windows PowerShellでは `./onebyone-cli.exe` に読み替えてください。開発ビルドの実行ファイルは `build/bin/` にあります。CLIを移動する場合は同梱の `tools/`・`bin/` も保持してください。

## 最初の実行

対象は初回コミットがあり、未コミット変更のないGitリポジトリ、またはその中のフォルダです。Git・ripgrepは配布物に同梱します。ルールで指定するビルド・テスト用ツールは対象プロジェクトに応じて用意します。

```sh
./onebyone doctor
./onebyone workspace validate --root ./project
./onebyone workspace create --name "API更新" --root ./project
./onebyone rules import --input ./api-update.oborules --mode replace
```

続いて、接続定義をJSONで用意します。Azure OAuthの例です。テナントID・クライアントIDには実際のGUIDを指定します。Entra側の登録・アクセス許可は[Azure OAuthの設定](azure-oauth.md)を参照してください。

```json
{
  "name": "Azure 開発用",
  "provider": "azure",
  "endpoint": "https://your-resource.openai.azure.com/openai/v1/",
  "deployment": "your-deployment",
  "authMode": "oauth",
  "oauthTenantId": "<テナントID>",
  "oauthClientId": "<クライアントID>"
}
```

この内容を `connection.json` として保存します。

```sh
./onebyone llm save --input ./connection.json
./onebyone llm list --json
./onebyone llm login --id <接続ID>
./onebyone llm test --id <接続ID>
./onebyone llm select --id <接続ID>
./onebyone scan
./onebyone status --json
./onebyone selection --all
./onebyone run --limit 20
./onebyone runs list --json
./onebyone report --output ./result.json
```

`llm list`の `id` が接続IDです。`login`は標準ブラウザーを開き、本人のサインインを待ちます。`test`は実際にモデルへリクエストを送り、利用料が発生する場合があります。`scan`までではLLMを呼びません。

`run`は選択済みの未修正・失敗ファイルを処理します。`--limit 20`は今回処理するファイルの上限で、省略または `0` は件数制限なしです。修正は専用Git作業コピーへコミットし、元の対象フォルダに自動でマージしません。

デモから始める場合は、上のワークスペース作成・ルール取り込み・抽出を次の1コマンドで置き換えられます。指定先の下に新しい子フォルダを作り、Gitの初回コミット・デモ用ルール・対象一覧まで準備します。LLM接続は別途設定します。

```sh
./onebyone demo --directory ./demo-projects --name "デモ"
```

`--directory`を省略するとOSの一時フォルダに作成します。既存のプロジェクトを上書きしません。

## コマンドと画面の対応

`--workspace <ID>`、`--config <PATH>`、`--json`は各コマンドで使え、コマンドの前後どちらにも置けます。ワークスペースIDを省略すると選択中のものを操作します。

| 画面・目的 | CLI |
| --- | --- |
| 環境確認 | `doctor` |
| ワークスペース一覧・切り替え | `workspace list` / `workspace select --id ID` |
| 新規・複製・名前変更 | `workspace create --name NAME --root PATH` / `workspace duplicate --name NAME` / `workspace rename --name NAME` |
| ワークスペース削除 | `workspace delete --id ID --yes` |
| 対象フォルダ検証・変更 | `workspace validate --root PATH` / `workspace target --root PATH` |
| LLM一覧・登録・選択 | `llm list` / `llm save --input JSON` / `llm select --id ID` |
| LLM認証・接続テスト | `llm login --id ID` / `llm logout --id ID` / `llm test --id ID` |
| LLMの保存済み認証情報を削除・接続を削除 | `llm clear --id ID --yes` / `llm delete --id ID --yes` |
| ルール一覧・詳細 | `rules list` / `rules show --id ID` |
| ルール追加・更新・削除 | `rules create --input JSON` / `rules update --input JSON` / `rules delete --id ID --yes` |
| パッケージ取り込み・書き出し | `rules import --input FILE.oborules --mode replace\|merge` / `rules export --output FILE.oborules` |
| 実行設定・抽出条件の確認・変更 | `settings show` / `settings update --input JSON` |
| 対象フォルダのファイル一覧・本文 | `files list` / `files show --file PATH` |
| 実行用作業コピーの本文 | `files show --file PATH --source execution` |
| 対象抽出・選択 | `scan` / `selection --file PATH` / `selection --all` / `selection --none` |
| 実行・現在の状態 | `run` / `status` |
| 再試行へ戻す | `retry --file PATH` / `retry --rule ID` |
| 実行履歴 | `runs list` / `runs show --id ID` |
| ファイルの差分・対応状況 | `detail --file PATH` / `detail --file PATH --run ID` |
| 結果JSONの出力 | `report --output FILE.json` / `report --run ID --output FILE.json` |
| ファイルの採用済み変更を破棄 | `discard --file PATH --yes` |
| 結果反映の一覧・本文を確認 | `publish preview` |
| 反映するファイルの差分を確認 | `publish diff --input JSON` |
| 新規ブランチへ1コミットで反映 | `publish create --input JSON` |

ファイルの `PATH` は対象フォルダからの相対パスです。操作の詳細は `./onebyone --help`、または `./onebyone <コマンド> --help` でも確認できます。

## LLM接続を登録する

`llm save`は `id` なしで新規登録、既存の `id` を指定すると更新します。更新時は変更する項目だけでなく、接続名・プロバイダー・エンドポイント・モデル・認証方式などの定義全体を渡します。保存とワークスペースへの選択は別の操作です。

| プロバイダー | `provider` | `authMode` | 保存済みキーがない場合の環境変数 |
| --- | --- | --- | --- |
| OpenAI | `openai` | `api_key` | `OPENAI_API_KEY` |
| Azure OpenAI / Microsoft Foundry | `azure` | `api_key` | `AZURE_OPENAI_API_KEY` |
| Azureの手動アクセストークン | `azure` | `bearer` | `AZURE_OPENAI_AUTH_TOKEN` |
| AzureのMicrosoftサインイン | `azure` | `oauth` | 使用しない。`llm login`で認証 |
| Claude | `claude` | `api_key` | `ANTHROPIC_API_KEY` |

APIキーを保存する場合はJSONの `credential` へ指定し、アクセス権を制限したJSONファイル、または `llm save --input -` の標準入力で渡します。キーやトークンをコマンド引数に指定するオプションはありません。環境変数を使う場合は `credential` を省略できます。

```sh
./onebyone llm save --input ./connection.json
./onebyone llm select --id <接続ID>
./onebyone run --connection <接続ID>
```

`run --connection`は今回使う登録済み接続を選択します。接続条件を手早く指定する場合は、`scan`・`run`に `--provider`、`--endpoint`、`--deployment`、`--auth-mode api_key|bearer`も使えます。選択中の定義と異なる場合はCLI用の別接続を登録して選択し、既存接続を上書きしません。OAuthは `llm save`・`llm login`を使います。

同じ接続条件の更新では、空の `credential` は保存済みキーを保持します。削除するには `llm clear --id ID --yes` を使ってください。OAuthの `logout` はローカルの接続キャッシュを削除し、ブラウザーのMicrosoftログインやMicrosoft側のトークン失効までは行いません。

キー・OAuthキャッシュは個人保存先で暗号化し、一覧・状態・レポートには出力しません。入力に使った外部JSONファイルはアプリが削除・暗号化しないため、保存する必要がなければ標準入力を使ってください。

## ルールと設定を編集する

`.oborules`はルール定義、旧シンボル、抽出条件、検証コマンドをまとめたパッケージです。取り込み時はワークスペース専用のコピーを作ります。

- `--mode replace`：ルール・パッケージ設定を置き換える。
- `--mode merge`：既存のルールに追加する。同じIDは取り込む側を `R019_2` などへ変更し、旧シンボルを重複なく追加する。既存の抽出条件・検証コマンドは維持する。

既存ルールがある場合は `--mode` が必須です。新規でルールがない場合の省略は `replace` として扱います。料金・処理上限はワークスペース側の設定として保持します。

ルールの追加JSONは次の形式です。`pattern` が空なら共通ルール、正規表現があれば個別ルールです。

```json
{
  "id": "R019",
  "name": "保存後に完了を通知する",
  "overview": "保存に成功した場合だけ、後続処理へ完了を通知する。",
  "before": "await store.save(record);",
  "after": "await store.save(record);\nawait events.saved(record.id);",
  "notes": "失敗時やリトライ途中には通知しない。周囲のエラー処理を維持する。",
  "holdConditions": "通知先や通知対象のIDを判断できない場合。",
  "pattern": "\\.save\\s*\\("
}
```

```sh
./onebyone rules create --input ./rule.json
./onebyone rules show --id R019 --json
./onebyone rules update --input ./rule-update.json
```

更新JSONは同じ項目に `expectedRevision` を追加し、直前の `rules show` が返したトップレベルの `revision` を指定します。表示結果の `rule.title` は更新入力では `name` に対応します。取得後に別の編集が入った場合や、他の利用者がロック中の場合は上書きせずエラーになります。

`settings update`は部分更新です。指定しない項目は維持し、配列は指定した内容へ置き換えます。

```json
{
  "maxTurns": 0,
  "timeoutSeconds": 0,
  "maxCostUSD": 0,
  "includeGlobs": ["src/**/*.js"],
  "excludeGlobs": ["**/generated/**"],
  "checkCommands": [
    {"name": "テスト", "executable": "node", "args": ["--test"]}
  ]
}
```

変更できる項目は `includeGlobs`、`excludeGlobs`、`checkCommands`、`maxAttempts`、`maxTurns`、`maxOutputTokens`、`maxFileBytes`、`timeoutSeconds`、`maxCostUSD`、`inputPricePerMillion`、`cachedInputPricePerMillion`、`outputPricePerMillion` です。ターン・時間・検証回数・料金上限の `0` は未設定です。出力トークン・ファイルサイズの `0` はアプリ既定値を使います。対象フォルダ、接続、ルールパッケージはそれぞれ専用コマンドで変更します。

ルールや抽出条件を変えたら `scan` で対象を更新し、選択を確認してから実行してください。`scan --root PATH` は対象フォルダを設定してから抽出します。ワークスペースがある場合は対象フォルダの変更、ない場合は作成を行います。

## 対象を選び、実行する

`files list`は対象フォルダのファイル一覧、`status --json`の `tasks` は抽出済みの処理候補です。`tasks`には状態、候補ルール、選択から除外したかを示す `excluded`、適用済みルール、履歴が含まれます。

```sh
./onebyone files show --file src/orders/save.js --source execution
./onebyone selection --file src/orders/save.js --file src/orders/send.js
./onebyone run
```

`selection`は今回指定したファイルだけを選択する操作で、選択への追加ではありません。`--all`・`--none`・`--file`は併用しません。新しく抽出されたファイルは既定で選択済みです。選択を外しても修正結果と履歴は残ります。

一度に同じワークスペースを変更できるのは1プロセスです。GUIが開いてロックしているワークスペースをCLIから開く場合も、編集・実行はできません。元プロセスで別のワークスペースへ切り替えるか終了してから操作します。

Ctrl+Cは処理の停止と保存を待って終了します。再開は同じワークスペースで `run` を実行します。完了・変更不要・要確認を明示的に再試行する場合は次のように戻します。

```sh
./onebyone retry --file src/orders/save.js
./onebyone retry --rule R019
./onebyone run
```

`retry --rule`はそのルールを適用済みのファイルを選びます。候補ルールの一致だけでは戻しません。`retry`自体はLLMを呼ばず、履歴を保持して未修正・選択済みへ戻します。

## 実行ごとの結果を見る

```sh
./onebyone runs list --json
./onebyone runs show --id <実行ID> --json
./onebyone detail --run <実行ID> --file src/orders/save.js --json
./onebyone detail --run <実行ID> --file src/orders/save.js --attempt 1 --json
./onebyone report --run <実行ID> --output ./result.json
```

`runs show`は実行情報、対象ファイル、当時の状態を返します。`detail`は変更前・変更後・差分と箇所別の対応状況を返し、`--attempt`なしなら初回から選択した実行までの累積表示です。`--attempt`はそのファイルの試行を1から数えた番号です。`--run`を省略すると現在の状態を使います。

`report`は接続の秘密情報を除いた結果JSONを保存します。`--output`を省略すると内部のレポート保存先へ出力し、生成したパスを返します。

`discard --file PATH --yes`は専用作業コピー上のそのファイルの採用済み変更を取り消すコミットを作ります。過去の履歴・証跡を消したり、元の対象フォルダを書き戻したりする操作ではありません。ワークスペース削除も対象ソースは削除せず、管理データを `workspace-trash/` へ退避します。

## 新規ブランチへ結果を反映する

作業コピーでの細かいコミットを、そのままレビューする必要はありません。`publish`は処理開始時のコミットを親にして、採用済みの最終差分を1コミットへまとめた新規ブランチを作成します。再試行の変更は累積し、破棄済みの変更・未採用の候補・差分がないファイルは含めません。反映対象は現在のワークスペースの最終結果全体です。実行履歴の選択や次回実行のチェック状態では絞り込みません。

まず一覧とコミット本文の初期値を確認します。この操作ではブランチを作成しません。

```sh
./onebyone publish preview --json
```

結果には `workspaceId`、`revision`、処理開始時の `baseCommit`、作業コピーの `sourceCommit`、ブランチ名の候補 `suggestedBranch`、本文の初期値 `message`、対象一覧 `files`、過去の反映記録 `publications` が含まれます。本文にはファイルごとの変更サマリーと実際に適用したルールIDを記載します。LLMを追加で呼び出す操作ではありません。

大量のファイルでも一覧を扱えるように、ファイルの差分は別途取得します。直前のプレビューの `workspaceId` と `revision`、確認したい相対パスを次のJSONへ入れて `diff.json` として保存します。

```json
{
  "workspaceId": "<確認したワークスペースID>",
  "revision": "<確認したプレビューのrevision>",
  "file": "src/orders/save.js"
}
```

```sh
./onebyone publish diff --input ./diff.json
```

反映するときは次のJSONを `publication.json` として保存します。`branch`には新しいブランチ名を、`title`にはコミットタイトルを必ず指定します。

```json
{
  "workspaceId": "<確認したワークスペースID>",
  "revision": "<確認したプレビューのrevision>",
  "branch": "review/storage-update",
  "title": "保存APIを更新する"
}
```

```sh
./onebyone publish create --input ./publication.json --json
```

`message`を省略すると、同じ `revision` のプレビュー本文を使用します。編集した本文を使う場合は `message` に文字列を指定します。明示的に `"message": ""` を指定した場合は本文なしです。入力は `--input -` で標準入力からも渡せます。プレビュー後に結果が変化した場合は反映せず、確認のやり直しを求めます。既存ブランチの上書きはできません。

成功時はブランチ名、作成したコミット、基点コミット、件数、タイトル・本文を返します。元フォルダのファイル・選択ブランチ・作業コピーの履歴を変更せず、チェックアウト・マージ・プッシュもしません。作成したブランチはアプリのワークツリーで使用しないため、元フォルダをSourceTreeなどで開いて、そのブランチへ切り替えてレビューできます。後から再試行・変更破棄をしても、既に反映したブランチは変わりません。更新した結果を反映する場合は、再度プレビューを取得して別名のブランチを指定します。

## 保存先・JSON・終了コード

既定ではGUIと同じローカル保存先を使います。Windowsは `%LOCALAPPDATA%/OneByOne/`、macOSは `~/Library/Application Support/OneByOne/` です。ワークスペース・パッケージ・キュー・結果はこの下で自動管理し、`--queue`・`--rules`・`--legacy`・`--rg`で直接保存先を差し替える方式は使いません。

アプリ設定の指定順は `--config`、環境変数 `ONEBYONE_CONFIG`、既定の `app-settings.json` です。指定した設定ファイルの親フォルダに `workspaces/` を作ります。個人接続の保存先は別で、検証環境を完全に分ける場合は `ONEBYONE_PRIVATE_DIR` も指定してください。

`--json`では標準出力をJSONとし、進捗ログとエラーを標準エラーへ出します。JSONの種類は操作ごとに異なり、例えば `status`は状態、`llm list`は接続一覧、`detail`はファイル詳細、`report`は保存先の `path` を返します。自動化では終了コードも確認してください。

| 終了コード | 意味 |
| --- | --- |
| `0` | 正常終了 |
| `1` | 操作・設定・実行開始などのエラー |
| `2` | 今回の実行対象に失敗・要確認が残った |
| `130` | Ctrl+Cなどで中断 |

過去の実行や今回除外したファイルの失敗だけで、今回の `run` を終了コード `2` にはしません。
