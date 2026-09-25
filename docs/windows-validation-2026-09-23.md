# Windows動作・ビルド検証（2026-09-23）

修正後の結果は[2026-09-24の再検証記録](windows-validation-2026-09-24.md)を参照。以下は修正前の記録。

Windows上でGUI・CLIをビルドでき、同梱Git・ripgrepによるデモ作成と対象抽出、GUI起動を確認した。一方、Windows固有のファイル操作・テストの失敗が残っており、配布可能とする判定には至っていない。アプリのソースコードは変更せず、不足を手動インストールで補う対応も行っていない。

## 環境と追加したもの

- VMware Fusion 26.0.1上のWindows 11 ARM64。2 CPU、メモリ4 GB。
- Go 1.27.1、Node.js 22.23.2、Wails 2.15.0のx64開発ツールを検証専用フォルダに用意し、Windowsのx64互換実行で`windows/amd64`をビルド・検証した。
- 開発ツールとは別に、Macから操作するためWindows OpenSSH Serverを設定した。鍵認証のみで、このMacのVM用アドレスからの接続に限定している。
- **アプリ実行用のGit・ripgrep・WebView2を手動追加インストールしていない。** Git・ripgrepは通常のPATHには存在せず、標準ビルド処理が準備した同梱物を使用した。
- WebView2 153.0.4234.48は、このWindowsに検証前から導入されていた。

検証用のルートは`%USERPROFILE%\OneByOneValidation\20260923`。ビルド用Go・Node.js・Wailsはここに配置し、OSの通常のPATHは変更していない。

## 確認結果

| 項目 | 結果 |
| --- | --- |
| フロントエンド型検査 | 成功 |
| Nodeテスト | 134件成功、1件失敗、9件スキップ |
| Goテスト | 11件の明示的失敗に加え、engineパッケージが10分で時間切れ。7パッケージ成功 |
| Go vet | 成功 |
| Windows上でのGUI・CLIビルド | 成功（約126秒） |
| 配布物CLIのスモークテスト | 成功。同梱Git/rg、デモ作成、23対象ファイル・13ルール、対象抽出、再起動後の状態復元を確認 |
| GUI | 起動、新規ワークスペース画面、Windowsのフォルダ選択、デモ作成、28ファイルの一覧・内容表示、13ルールの自動読み込みを確認 |
| MacからのWindows操作 | SSHサービスを自動起動に設定し、サービス経由の再接続成功を確認 |

GUIも専用の個人設定フォルダと、Windows・System32だけのPATHで起動した。開発用Go・Node.jsや外部Git/rgをGUIの動作に使わない条件で確認している。実際のLLMへの送信は行っていない。

実行したコマンドは次の通り。`npm test`はNodeテストの失敗で停止するため、Goの工程を同じ準備済み環境で別途実行した。

```powershell
npm.cmd test
npm.cmd run build -- --platform windows/amd64
node.exe scripts/smoke-package.mjs build/bin
go test ./internal/... ./cmd/...
go vet ./internal/... ./cmd/...
```

## 残っている問題

1. **並行読込中の保存が失敗する。** `TestAtomicSnapshotReadersNeverSeePartialWrites`で、スナップショットの置き換えが`Access is denied`になった。保存処理側のWindows対応を確認する必要がある。
2. **ワークスペースの削除操作が失敗する。** CLIのテストで、`workspaces/<id>`から`workspace-trash/<id>-...`への移動が`Access is denied`になった。どのハンドルが妨げたかは今回のログだけでは確定していない。
3. **ロックされたファイルを読むテストが失敗する。** engineで3件、storeで1件。テストがロックファイルを別ハンドルから読んでおり、Windowsでは拒否されている。これらはロックの取得・解放自体の不良と同一視しない。
4. **ワークスペースの比較テストが5件失敗する。** 内容・権限・更新日時などをまとめて比較する検査で不一致。現在のエラーには差異のあるパス・項目が出ず、内容変更かメタデータ差かは未特定。
5. **Nodeのパス比較がWindows表記に対応していない。** `scripts/bundled_git.test.mjs:139`で、期待値`../bin/git`に対して実値`..\bin\git`となる。
6. **Goのengine全体が10分に収まらない。** 時間切れ時のテストは開始から5秒で、`git worktree add`を処理中だった。直前にも新しいテストへ進んでいたため、1件が10分間停止した証拠ではなく、パッケージ全体の時間制限に達したと判断する。x64互換実行やVM性能の寄与は推測であり、比較計測はしていない。上限を延ばすだけでは上記の失敗は解決しない。

Goの明示的失敗はengine 8件、store 2件、CLI 1件。engineは途中で打ち切られており、未実行部分の合否は不明。

## 検証範囲の限界

- Intel/AMDの物理Windows端末、ネイティブWindows ARM64配布物は未検証。
- Setupインストーラーの作成・導入・更新・アンインストール、WebView2が未導入の環境は未検証。今回起動したのは`build/bin`の生成物。
- LLM接続、実際のAI修正、結果反映までの実サービスを通した一連の実行は未検証。
- GUIではフォルダ選択ダイアログが前面に出ない場面があり、タスクバーからアプリを選び直して操作できた。検証用コンソールとの前面競合もあったため、アプリ単体の再現条件は確定していない。

## 生成物・証跡

Windows生成物は`%USERPROFILE%\OneByOneValidation\20260923\source\build\bin`の`OneByOne.exe`、`onebyone-cli.exe`と同梱ランタイム。

Mac側の`build/validation/windows-20260923/`へ、次のログを原文のまま保存した。これらは端末内の検証記録として保持し、Git管理から除外している。

- `validation-summary.json`：テスト・ビルド・スモークの終了コードと時間
- `test.log`、`build.log`、`smoke.log`
- `go-test.log`、`go-checks-summary.json`

SSH設定と接続先は検証端末の個人設定で管理し、リポジトリには含めない。サービスは自動起動で、接続許可先はVM用ネットワーク上の検証用Macのみ。
