# 開発者向けビルド・配布手順

WindowsとmacOSで、リポジトリ直下の `npm test`、`npm run build`、`npm run package` を共通で使います。Git・ripgrepはアプリとCLIに同梱し、WindowsのSetupにはWebView2のオフライン導入用ファイルも含めます。利用者がGo・Node.js・Git・rgを別途インストールする必要はありません。対象プロジェクトのビルド・テストに使うツールと依存関係は別途必要です。

Goテストのパッケージごとの制限時間はWindowsで30分、その他で10分です。多数のGit操作を含むため、Windowsの仮想環境でも全体を検査できる時間を確保しています。`npm test -- --timeout 45m`で変更できます。これは開発用テストの制限で、アプリのAI処理上限とは別です。

## 初回の準備

開発環境にはGo 1.27.1、Node.js 22、Wails v2.15.0を用意します。macOSではXcode Command Line Tools、WindowsでGUIを動かす場合はWebView2が必要です。WindowsのSetup生成にはNSIS 3を使います。[Wailsの環境構築](https://wails.io/docs/gettingstarted/installation/)も参照してください。

```text
go install github.com/wailsapp/wails/v2/cmd/wails@v2.15.0
npm --prefix frontend ci
```

NSISはWindowsでは `choco install nsis --yes`、MacからWindowsのSetupを生成する場合は `brew install nsis` で用意できます。`makensis` がPATHにない場合は `MAKENSIS_BINARY` または `--makensis` で指定します。Go・Wailsも `GO_BINARY`・`WAILS_BINARY` で指定できます。

ルートのpackage.jsonには追加のnpm依存関係がないため、ルートでの `npm install` は不要です。フロントエンドのロックファイルを更新したら `npm --prefix frontend ci` を再実行します。

## 同梱資材の取得

ビルド・テスト・パッケージの各コマンドは、`build/runtime-lock.json` に固定したURL・SHA256から対象OS・CPU用の資材を取得します。先に取得だけ済ませる場合は次を実行します。

```text
npm run runtime:prepare
npm run runtime:prepare -- windows amd64
```

- macOSのGitはdugite-native、WindowsのGitはMinGitを使い、補助実行ファイル・ライブラリ・テンプレートを含む配布一式を保持します。
- rgは公式リリースの実行ファイルと原文ライセンスを使います。
- WindowsのSetupにはMicrosoftのWebView2 Evergreen Standalone Installerを含めます。小さいオンライン用bootstrapperとは別の資材です。
- Git本体の対応ソースと取得元情報も同梱します。ライセンス資材の範囲は後述します。

キャッシュは `.cache/runtimes/` です。`ONEBYONE_RUNTIME_CACHE` で変更できます。取得時と再利用時にSHA256を照合し、展開済みファイルも内容・モード・リンク先を検査します。破損や手動変更を検出したら停止します。初回取得にはネットワーク接続が必要で、一度準備した同一資材は再利用します。**配布後のアプリ起動時にGit・rgをダウンロードする処理はありません。**

依存バージョンを更新するときは、ロックファイルのURL・ハッシュ・対応ソース・ライセンスをまとめて更新し、両OSのネイティブテストを実行します。自動で最新版へ切り替えません。

組織内で準備済みの資材を使う場合は `RG_BINARY`・`RG_LICENSE_DIR`・`RG_VERSION`・`RG_SHA256`、`GIT_BUNDLE_DIR`・`GIT_BUNDLE_VERSION`・`GIT_BUNDLE_SHA256`・`GIT_BUNDLE_SOURCE` で取得を置き換えられます。GitのSHA256は取得アーカイブ、rgのSHA256は展開後の実行ファイルを指します。元のGit配布には `licenses/` と `sources/` の資材も必要です。WebView2は `WEBVIEW2_INSTALLER`・`WEBVIEW2_SHA256` で指定できます。通常はこれらの手動指定は不要です。

## テスト・ビルド・パッケージ

```text
npm test
npm run build
npm run package
```

`npm test` はフロントエンドの型検査、Nodeテスト、Goテスト、Go vetを順に実行します。用意した同梱Git・rgを使い、ローカルのモックAPIと使い捨てGitリポジトリで確認します。実サービスのAPIキーは不要です。`npm test -- --race` ではGoのrace detectorも使います。対応するネイティブCコンパイラが必要です。

`npm run build` はフロントエンド、Wails GUI、CLIを作り、Wailsのpost-build hookでGit・rgを配置します。Gitの本体だけでなく補助バイナリのCPUやリンク先も検査します。macOSの最小OSは13.0です。

`npm run package` はビルドに続いてGUI・CLI・同梱資材・原文ライセンス・README・docsを梱包します。Windowsではフォルダ・ZIPに加えてSetupを生成します。

| 対象 | GUI | 配布物 |
| --- | --- | --- |
| macOS | `build/bin/OneByOne.app` | `build/package/OneByOne-macos-<CPU>.zip` |
| Windows | `build/bin/OneByOne.exe` | `build/package/OneByOne-windows-<CPU>-Setup.exe` とZIP |

CLIはmacOSが `onebyone`、Windowsが `onebyone-cli.exe` です。GUIとCLIで大文字小文字を区別しないファイル名の衝突を避けています。Gitは実行ファイルと同じ場所の `tools/git/`、rgは `bin/` に配置します。macOSの `.app` 内部にも同じ資材を配置するため、アプリだけをApplicationsへ移動して利用できます。

同梱Gitがある配布では外部Gitへ切り替えません。同梱ファイルが不完全なら修復・再インストールを案内します。開発・Goテストのように同梱ランタイムがない場合だけPATHのGitを使います。Gitコマンドとその子プロセスは同梱helperを参照し、個人のGit設定やフック・署名設定が処理に混ざらないようにします。

### WindowsのSetup

Setupは現在のユーザーの `%LOCALAPPDATA%/Programs/OneByOne/` へインストールし、スタートメニューとアンインストール情報を作ります。管理者権限を要求しない構成です。WebView2が未導入またはWailsの最小要件未満の場合だけ、同梱のStandalone Installerを実行します。導入結果を確認できなければアプリの配置を中断します。必要なWebView2がすでにあれば再インストールしません。端末の管理ポリシーによる制約は受けます。

この方式は[Microsoftのオフライン配布方法](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/distribution)に沿っています。WebView2は導入後にEvergreenの更新機構で更新される共有ランタイムで、OneByOne専用のFixed Versionではありません。アンインストール時はOneByOneの配布ファイルだけを削除し、利用者のワークスペース・認証設定・共有WebView2は保持します。

更新時は登録済みの旧アンインストーラーで旧配布ファイルを削除してから展開するため、削除されたGit helperが残りません。起動中・書き込み不可・リンクへの置き換えを検出した場合は中断します。利用者が追加したファイルは再帰削除しません。

通常の利用者にはZIPではなくSetupを渡します。ZIPを直接展開する場合はWebView2が別途導入済みである必要があります。Wailsの `-webview2 embed` はオンライン導入用の補助であり、WebView2の全ランタイムをexeに内蔵する指定ではありません。

### 対象指定・再梱包・署名

```text
npm run package -- --out build/package/OneByOne-local
npm run package -- --platform windows/amd64 --out build/package/OneByOne-windows
npm run package -- --skip-build --out build/package/OneByOne-existing
npm run package -- --no-adhoc-sign --no-archive --no-installer --out build/package/OneByOne-for-signing
npm run help
```

既定は実行中のOS・CPUです。`--platform` は `macos/arm64`、`macos/amd64`、`windows/amd64`、`windows/arm64` に対応します。`darwin` は `macos` の別名です。MacアプリはmacOS上でビルドします。MacからWindowsへのクロスビルドも同じコマンドで行えますが、Windowsの実動作確認にはなりません。既存出力は上書きしません。

macOSでは同梱ライブラリを含めてアドホック署名を作り直し、署名後に同梱Gitのinit・commit・worktreeを実行します。正式なDeveloper ID署名・公証は別工程です。WindowsのSetupも組織の証明書によるコード署名は行いません。WebView2の取得ファイルは全OSでSHA256を照合し、WindowsでのSetup生成時はMicrosoftのAuthenticode署名も検証します。

`build-info.json` と `tools/runtime.json` に資材のバージョン・取得元・ハッシュ・検証方法を記録します。クロスビルド時は外国OSのバイナリを実行せず、実行形式のヘッダーとハッシュを検査したことを記録します。

### ライセンス資材

rgの原文は `licenses/ripgrep/`、Git配布の原文ライセンスとNOTICEは `tools/git/licenses/` および元の配布内の各ライセンスフォルダに保持します。Git本体のソースとビルド資材は `tools/git/sources/` に保存します。Go・フロントエンドの通知は `THIRD_PARTY_LICENSES.txt` にまとめています。

同梱資材の取得元・ライセンスを削除しないでください。Gitの配布に含まれる補助コンポーネントも含め、外部公開前のライセンス確認と必要な対応ソースの整備を配布工程に含めます。

## 利用者のローカル設定とルール配布

配布物に利用者の設定を同梱しません。Windowsは`%LOCALAPPDATA%/OneByOne/`、macOSは`~/Library/Application Support/OneByOne/`をアプリ用保存先に使います。ワークスペースは`workspaces/<ID>/setting.json`、取り込んだルールはその配下の`rule-packages/<ID>/`、既定のキュー・結果は`runs/`に保存します。

個人LLM接続は`private/llm-settings/<ハッシュ>.json`に接続定義の一覧をまとめ、APIキー・OAuthトークンキャッシュも含めてAES-256-GCMで暗号化します。初回生成する鍵は`private/master-key.json`に保存します。Windowsでは現在のユーザーのDPAPI、macOSでは所有者だけがアクセスできる権限（フォルダ`0700`・ファイル`0600`）で鍵を保護します。macOS Keychainは使わず、鍵と暗号化データの両方を取得できれば復号できます。

AzureのOAuthにはMSAL Goを使用します。MicrosoftサインインはOSの標準ブラウザーと一時的なlocalhost待受で行い、Goバイナリ以外の認証ツール・Azure CLIは不要です。利用者は自分の組織のテナントID・クライアントIDを設定します。配布物へ共通のクライアントシークレットや利用者の認証キャッシュを埋め込まないでください。[Azure OAuthの設定](azure-oauth.md)にアプリ登録・権限・保存方式をまとめています。

検証では`ONEBYONE_CONFIG`と`ONEBYONE_PRIVATE_DIR`をそれぞれ使い捨てローカル保存先へ設定します。`ONEBYONE_CONFIG`の親フォルダがワークスペースの保存先になります。個人LLM保存先は`ONEBYONE_PRIVATE_DIR`で別途指定します。実際のキー、個人接続一覧、利用者のワークスペースを配布物へ混ぜないでください。

ルールの配布には`.oborules`を使います。これはルール資材と`package.json`に処理設定をまとめたZIPで、LLM接続・対象フォルダ・実行結果の保存先を含めません。読み込み先では専用コピーを作り、元の配布ファイルを変更しません。形式と設定キーは[設計書](design.md)に記載しています。

新規ワークスペースは対象フォルダを先に選び、設定を端末内に保存します。同じローカルワークスペースを別アプリから開くと、OSロックによって後から開いた側は閲覧専用になります。利用者が外部へ実行結果を保存する場合は、保存先とGitリポジトリへの必要なアクセス権を利用環境で用意します。

## CIと配布時の確認

`.github/workflows/build.yml` は `macos-15` と `windows-2025` のネイティブ環境で、共通の `npm test` と `npm run package` を実行します。macOSのテストはrace detectorを有効にします。WindowsではNSISを導入してSetupまで生成し、ZIPとともに成果物へ保存します。バージョン固定した取得資材はSHA256単位でキャッシュします。

梱包後は `node scripts/smoke-package.mjs <配布フォルダ>` で、外部Git・rgをPATHに含めずにCLIでデモ作成・ルール読込・対象抽出・再起動後の状態復元を確認します。テスト用の設定・一時ファイルは隔離して削除し、LLMへリクエストを送信しません。

CIはpush・pull request・手動実行で起動します。設定ファイルを追加したことと実際のCI成功は別です。この変更時にはMac上のテスト・配布物生成・CLI smokeを確認しています。WindowsのクロスビルドとSetupコンパイルは確認していますが、Windows実機のインストール・GUI起動・アンインストールは未確認です。WindowsのGit未導入・WebView2未導入環境での初回導入、導入済み環境での更新、フォルダ選択、停止・再開を実機で確認してください。

ワークスペース機能については、少なくとも次を確認します。

- 新規作成の「デモプロジェクトを作成する」で保存先を選び、新しい子フォルダに28ファイルとGitの初回コミットが作られ、対象欄に反映される。キャンセル・作成失敗では元の選択を維持する。同じ保存先で再作成しても以前のデモや既存ファイルを上書きしない。
- デモのワークスペース作成時に共通3件・個別10件を自動設定し、「実行設定」で23ファイルが抽出される。`src/workflows/dispatch-cycle.js`が全13ルールの候補になることを確認する。デモの作成・読込・抽出だけではLLMに送信しない。
- 「＋」の新規作成で、対象フォルダ選択前とGitでないフォルダ・初回コミットのないフォルダ・未コミット変更のあるフォルダを選んだ場合は作成できず、フォルダ欄に理由を表示する。ステージ済みの変更・未追跡ファイル・選択したサブフォルダ外の変更も確認し、Gitで除外された未追跡ファイルは許可する。作成後の「対象フォルダ」の変更でも同じ検証を行い、失敗時は元の対象と結果を保持する。変更に成功した場合は以前の結果を保持して再抽出が必要になる。選択後にソースを変更した場合は実行開始時にも停止する。
- 「＋」は新規作成画面を直接開き、保存済みのワークスペースは起動時に読み込んで左の一覧から選択する。外部のワークスペースを探索・追加する機能は設けない。
- 個人設定を再起動後も利用でき、ワークスペースJSONには非秘密の接続IDだけを保存し、キュー・ルールパッケージ・配布物にはLLM接続を含めない。保存済み認証情報の削除と環境変数の扱いも確認する。
- OAuthはローカルのモックで、認証情報の暗号化・非露出、トークン更新、取消、サインアウトと更新の競合、編集・独立レビュー・接続テストの認証経路を検証する。実際のMicrosoftアカウントによるサインイン・MFA・組織の条件付きアクセス・リソースへの推論は別途実環境で確認する。本変更時には実アカウントによるサインインは実施していない。
- 別プロセスから同じワークスペースを開くと閲覧専用になり、先行プロセスの切替・終了後に編集権限を取得できる。
- `.oborules`を別のローカルワークスペースに読み込み、設定・補助資材を復元できる。壊れたZIPや不正なパスでは設定と元パッケージを変更しない。
- ルールを編集・保存すると候補の再抽出が必要になり、再起動後もその条件を維持する。別アプリや古い編集画面からの競合保存を拒否する。
