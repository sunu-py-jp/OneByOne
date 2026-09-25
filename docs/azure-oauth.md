# Azure OAuth の設定

Azure OpenAI / Microsoft Foundry では、APIキーの代わりに自分のMicrosoftアカウントでサインインできます。OneByOneはMicrosoft Entra IDの認証コードフローとPKCEをMSAL Goで実行します。クライアントシークレットやAzure CLIは不要です。

## 1. 組織側で準備する

対象のAzureリソースを利用できる組織アカウントと、OneByOne用のEntraアプリ登録が必要です。組織に既存の登録があれば、そのテナントID・クライアントIDを管理者から受け取ります。

新しく登録する場合は、Microsoft Entra管理センターの「アプリの登録」で次を設定します。

1. 対象リソースのテナントでアプリを登録する。単一組織で使う場合は、その組織内のアカウントを対象にする。
2. 「認証」→「プラットフォームを追加」→「モバイルとデスクトップ アプリケーション」を選び、リダイレクトURIに **`http://localhost`** を登録する。WebやSPAとして登録しない。
3. 「概要」にある「ディレクトリ（テナント）ID」と「アプリケーション（クライアント）ID」を控える。両方ともGUID形式の値を使う。クライアントシークレットは作成しない。

ブラウザー認証の戻り先には空いているローカルポートを使います。Entraのlocalhost照合ではポート番号が無視されるため、固定ポートの指定は不要です。[Microsoftのデスクトップアプリ登録手順](https://learn.microsoft.com/en-us/entra/identity-platform/scenario-desktop-app-configuration)、[localhostのリダイレクト仕様](https://learn.microsoft.com/en-us/entra/identity-platform/reply-url)

「APIのアクセス許可」では「所属する組織で使用しているAPI」から **Azure Machine Learning Services** を探し、委任されたアクセス許可 **`user_impersonation`** を追加します。OneByOneが要求するスコープは `https://ai.azure.com/.default` です。組織のポリシーに応じて管理者の同意も必要です。このスコープの同意先は、名前の似たMicrosoft Cognitive Servicesとは異なります。[Microsoft公式サンプルの同意設定](https://github.com/microsoft-foundry/foundry-agent-webapp#rbac--consent-requirements)

さらに、**サインインするユーザー本人**へ対象リソースのIAMからモデル利用権限を付与します。

| 利用するモデル | 権限の例 |
|---|---|
| Azure OpenAIのモデル | Cognitive Services OpenAI User |
| Foundryの他社モデルも含む推論 | Cognitive Services User |

OwnerやContributorだけでは推論権限を満たさない場合があります。ロールの反映には最大5分ほどかかることがあります。[Azure OpenAIの権限](https://learn.microsoft.com/en-us/azure/foundry-classic/openai/how-to/managed-identity)、[Foundryの認証・権限](https://learn.microsoft.com/en-us/azure/foundry/foundry-models/how-to/configure-entra-id)

## 2. OneByOneでサインインする

1. 左メニューの「LLM接続」で接続を追加し、プロバイダーに「Azure OpenAI / Microsoft Foundry」を選ぶ。
2. 接続名、エンドポイント、モデルのデプロイ名を入力する。
3. 認証方式を「Microsoft Entra ID（OAuth）」にし、テナントIDとクライアントIDを入力する。
4. **「Microsoftでサインイン」** をクリックする。設定が保存され、標準ブラウザーでMicrosoftの認証画面が開く。
5. 利用権限のあるアカウントを選び、必要な認証・同意を完了する。アプリにアカウント名が表示されたら「接続をテスト」でモデルへの接続を確認する。
6. ワークスペースの「実行設定」で、このLLM接続を選ぶ。

中止する場合はアプリの「キャンセル」を押します。認証待ちには10分の上限があります。キャンセル後に遅れて返ってきた認証情報は保存しません。画面プレビューでは実際のサインインは行えません。

対応するOAuthエンドポイントは、Azureパブリッククラウドの次のリソースURLです。HTTPSを使用し、ポートは未指定または443にします。

```text
https://<resource>.openai.azure.com/openai/v1/
https://<resource>.services.ai.azure.com/openai/v1/
https://<resource>.cognitiveservices.azure.com/openai/v1/
```

リソースのルートURL、または`/openai/v1/responses`までのURLも指定できます。利用するAPIが実際に提供されているリソースURLを使ってください。任意のプロキシURL、ローカルURL、独自ポート、Azure Government / 中国クラウドは今回のOAuth対応範囲に含めません。OpenAI・Claudeは引き続きAPIキー認証です。

## 3. 保存・更新・サインアウト

サインインは接続単位で保存され、再起動後も再利用します。編集処理・独立レビュー・接続テストは同じ接続を使い、各リクエスト前に有効なトークンを取得します。期限が近い場合などはMSALが保存済みキャッシュを使って更新します。CLIも同じ接続を再利用し、GUIを開かずに `llm login --id <接続ID>` でサインインできます。接続登録からの手順は[CLIの使い方](cli.md)を参照してください。

更新できない場合は再サインインが必要です。処理中にブラウザーを自動起動したり、別アカウントやAPIキー・環境変数へ自動で切り替えたりはしません。手動入力する「Microsoft Entra ID アクセストークン」は別の認証方式で、自動更新の対象ではありません。

「サインアウト」は**この接続に保存した認証情報を削除**します。ブラウザーのMicrosoftログイン状態や他の接続は維持し、Microsoft側のトークン失効は行いません。接続先・テナントID・クライアントID・認証方式を変更した場合も、以前の認証情報は引き継がず再サインインします。

APIキーとOAuthキャッシュは、端末内の `private/llm-settings/` に接続設定とまとめて **AES-256-GCMで暗号化**して保存します。ハッシュ化ではなく、API呼び出し時にはGo側のメモリで復号して使用します。トークンを画面・ログ・ワークスペース・ルールパッケージへ渡しません。

暗号化鍵は初回にランダムな32バイトとして生成します。Windowsは現在のユーザーのDPAPIで鍵も保護します。macOSは所有者専用のファイル権限で鍵を保護し、**Keychainは使用しません**。macOSで鍵と暗号化ファイルの両方を取得できる人は復号できます。詳細は[個人設定の保存方式](../internal/privateconfig/README.md)を参照してください。

## 接続できない場合

| 症状 | 確認する内容 |
|---|---|
| ブラウザーでサインインできない | テナントID・クライアントID、デスクトップ用`http://localhost`の登録、委任アクセス許可と同意、組織の条件付きアクセス |
| サインイン後、接続テストで401・403 | ユーザー本人のリソース権限、ロールの反映待ち、エンドポイントとデプロイ名 |
| 自動更新できない | ネットワークを確認し、LLM接続で再サインインする |
| 別のアカウントを使いたい | 「別のアカウントでサインイン」から選び直す |

自動テストではローカルのモックを使用します。実際のMicrosoftアカウントでのサインイン、MFA、組織の条件付きアクセス、Azureリソースへの推論は未確認です。
