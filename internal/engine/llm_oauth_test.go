package engine

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"onebyone/internal/azureauth"
	"onebyone/internal/model"
	"onebyone/internal/privateconfig"
)

func oauthTestConnection(t *testing.T, s *Service) model.LLMConnection {
	t.Helper()
	connection := model.LLMConnection{
		Name: "Microsoft test connection", Provider: "azure", AuthMode: "oauth",
		Endpoint: "https://oauth-test.openai.azure.com/openai/v1/", Deployment: "test-deployment",
		OAuthTenantID: "12345678-1234-1234-1234-123456789abc",
		OAuthClientID: "98765432-4321-4321-4321-abcdef123456",
	}
	state, err := s.SaveLLMConnection(connection)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.LLMConnections) != 1 {
		t.Fatal("OAuth connection was not registered")
	}
	return state.LLMConnections[0]
}

func oauthTestSession() azureauth.Session {
	return azureauth.Session{
		Cache:     []byte(`{"RefreshToken":{"mock":{"secret":"PRIVATE-REFRESH-TOKEN"}}}`),
		AccountID: "private-account-id", Username: "oauth-user@example.test",
	}
}

func oauthTestSignIn(t *testing.T, s *Service, connection model.LLMConnection) model.State {
	t.Helper()
	s.oauthSignIn = func(context.Context, azureauth.Settings) (azureauth.Session, error) {
		return oauthTestSession(), nil
	}
	state, err := s.SignInLLMConnection(connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.LLMConnections[0].OAuthSignedIn || !state.LLMConnections[0].CredentialSet {
		t.Fatal("OAuth sign-in did not publish authenticated state")
	}
	return state
}

func oauthTestSavedConnection(t *testing.T, s *Service, id string) privateconfig.Connection {
	t.Helper()
	registry, err := s.personal.LoadConnections()
	if err != nil {
		t.Fatal(err)
	}
	connection, err := connectionByID(registry, id)
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func TestOAuthEncryptedSessionSurvivesRestartWithoutSerializingRuntimeTokens(t *testing.T) {
	s, root := workspaceTestService(t)
	connection := oauthTestConnection(t, s)
	state := oauthTestSignIn(t, s, connection)
	private := oauthTestSession()
	assertPersonalCiphertexts(t, state.LLMSettingsPath, "PRIVATE-REFRESH-TOKEN", private.AccountID, private.Username, connection.OAuthTenantID, connection.OAuthClientID)
	if _, err := s.CreateWorkspace("OAuth workspace", root); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectLLMConnection(connection.ID); err != nil {
		t.Fatal(err)
	}
	s.Close()
	restarted := workspaceNewService(t, s.configPath)
	state = restarted.Snapshot()
	if state.LastError != "" || state.SelectedLLMConnectionID != connection.ID || !state.LLMConnections[0].OAuthSignedIn {
		t.Fatalf("OAuth session did not survive restart: %s", state.LastError)
	}
	saved := oauthTestSavedConnection(t, restarted, connection.ID)
	if saved.OAuthAccountID != private.AccountID || string(saved.OAuthCache) != string(private.Cache) || saved.OAuthGeneration == "" {
		t.Fatal("encrypted OAuth session changed across restart")
	}
	acquired := 0
	restarted.oauthAcquire = func(_ context.Context, settings azureauth.Settings, session azureauth.Session) (string, azureauth.Session, error) {
		acquired++
		if settings.TenantID != connection.OAuthTenantID || settings.ClientID != connection.OAuthClientID || session.AccountID != private.AccountID {
			t.Fatal("runtime used a different OAuth identity")
		}
		session.Cache = []byte(`{"RefreshToken":{"mock":{"secret":"ROTATED-REFRESH-TOKEN"}}}`)
		return "EPHEMERAL-ACCESS-TOKEN", session, nil
	}
	config, err := restarted.oauthRunConfig(context.Background(), state.Config)
	if err != nil || config.AcquireToken == nil || config.Credential != "" || acquired != 1 {
		t.Fatalf("OAuth runtime was not initialized safely: %v", err)
	}
	if token, err := config.AcquireToken(context.Background()); err != nil || token != "EPHEMERAL-ACCESS-TOKEN" {
		t.Fatalf("OAuth runtime did not acquire token: %v", err)
	}
	for _, value := range []any{config, restarted.Snapshot(), safeConfig(config)} {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"PRIVATE-REFRESH-TOKEN", "ROTATED-REFRESH-TOKEN", "EPHEMERAL-ACCESS-TOKEN", "private-account-id", "oauthCache", "AcquireToken"} {
			if strings.Contains(string(body), secret) {
				t.Fatal("public/runtime serialized state leaked OAuth credentials")
			}
		}
	}
	if safeConfig(config).AcquireToken != nil {
		t.Fatal("public config retained runtime token provider")
	}
	if err := restarted.persist(); err != nil {
		t.Fatal(err)
	}
	report, err := restarted.ExportReport("")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		workspaceSettingPath(t, restarted, state.ActiveWorkspaceID),
		state.Config.QueuePath + ".session.json", report,
	} {
		body := readTest(t, path)
		for _, private := range []string{"PRIVATE-REFRESH-TOKEN", "ROTATED-REFRESH-TOKEN", "EPHEMERAL-ACCESS-TOKEN", "private-account-id", "oauthCache", connection.OAuthClientID, connection.OAuthTenantID} {
			if strings.Contains(body, private) {
				t.Fatal("workspace or report leaked OAuth credentials")
			}
		}
	}
	assertPersonalCiphertexts(t, state.LLMSettingsPath, "ROTATED-REFRESH-TOKEN", "EPHEMERAL-ACCESS-TOKEN")
	refreshed := oauthTestSavedConnection(t, restarted, connection.ID)
	if !strings.Contains(string(refreshed.OAuthCache), "ROTATED-REFRESH-TOKEN") || strings.Contains(string(refreshed.OAuthCache), "EPHEMERAL-ACCESS-TOKEN") {
		t.Fatal("refreshed cache was not persisted separately from runtime token")
	}
}

func TestOAuthRenamingPreservesSessionAndIdentityChangesClearIt(t *testing.T) {
	for _, change := range []string{"endpoint", "tenant", "client", "authentication"} {
		t.Run(change, func(t *testing.T) {
			s, _ := workspaceTestService(t)
			connection := oauthTestConnection(t, s)
			oauthTestSignIn(t, s, connection)
			before := oauthTestSavedConnection(t, s, connection.ID)
			connection.Name = "Renamed connection"
			connection.Deployment = "another-deployment"
			if _, err := s.SaveLLMConnection(connection); err != nil {
				t.Fatal(err)
			}
			renamed := oauthTestSavedConnection(t, s, connection.ID)
			if renamed.OAuthGeneration != before.OAuthGeneration || renamed.OAuthAccountID != before.OAuthAccountID || string(renamed.OAuthCache) != string(before.OAuthCache) {
				t.Fatal("nonidentity edit discarded an OAuth session")
			}
			switch change {
			case "endpoint":
				connection.Endpoint = "https://another.services.ai.azure.com/openai/v1/"
			case "tenant":
				connection.OAuthTenantID = "abcdef12-1234-1234-1234-123456789abc"
			case "client":
				connection.OAuthClientID = "abcdef12-4321-4321-4321-abcdef123456"
			case "authentication":
				connection.AuthMode = "api_key"
			}
			state, err := s.SaveLLMConnection(connection)
			if err != nil {
				t.Fatal(err)
			}
			changed := oauthTestSavedConnection(t, s, connection.ID)
			if state.LLMConnections[0].OAuthSignedIn || len(changed.OAuthCache) != 0 || changed.OAuthAccountID != "" || changed.OAuthUsername != "" || changed.OAuthGeneration == before.OAuthGeneration {
				t.Fatal("identity change kept the old OAuth session")
			}
		})
	}
}

func TestOAuthCanceledLateSignInCannotSaveCredentials(t *testing.T) {
	s, _ := workspaceTestService(t)
	connection := oauthTestConnection(t, s)
	before := oauthTestSavedConnection(t, s, connection.ID)
	entered, release := make(chan struct{}), make(chan struct{})
	s.oauthSignIn = func(context.Context, azureauth.Settings) (azureauth.Session, error) {
		close(entered)
		<-release // Deliberately simulate an SDK completion racing cancellation.
		return oauthTestSession(), nil
	}
	result := make(chan error, 1)
	go func() {
		_, err := s.SignInLLMConnection(connection.ID)
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("sign-in did not start")
	}
	s.CancelLLMSignIn()
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("late sign-in was not canceled: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled sign-in did not finish")
	}
	after := oauthTestSavedConnection(t, s, connection.ID)
	if len(after.OAuthCache) != 0 || after.OAuthAccountID != "" || after.OAuthGeneration != before.OAuthGeneration || s.Snapshot().LLMConnections[0].OAuthSignedIn {
		t.Fatal("late sign-in resurrected canceled credentials")
	}
}

func TestOAuthPeerSignOutPreventsInFlightRefreshResurrection(t *testing.T) {
	first, _ := workspaceTestService(t)
	connection := oauthTestConnection(t, first)
	oauthTestSignIn(t, first, connection)
	second := workspaceNewService(t, filepath.Join(t.TempDir(), "peer-settings.json"))
	frozen := oauthTestSavedConnection(t, first, connection.ID)
	entered, release := make(chan struct{}), make(chan struct{})
	first.oauthAcquire = func(_ context.Context, _ azureauth.Settings, session azureauth.Session) (string, azureauth.Session, error) {
		close(entered)
		<-release
		session.Cache = []byte(`{"RefreshToken":{"mock":{"secret":"LATE-ROTATED-REFRESH"}}}`)
		return "LATE-ACCESS-TOKEN", session, nil
	}
	type outcome struct {
		token string
		err   error
	}
	result := make(chan outcome, 1)
	provider := first.oauthTokenProvider(frozen)
	go func() { token, err := provider(context.Background()); result <- outcome{token: token, err: err} }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not start")
	}
	if _, err := second.SignOutLLMConnection(connection.ID); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	select {
	case result := <-result:
		if result.token != "" || !errors.Is(result.err, errOAuthConnectionChanged) {
			t.Fatalf("stale refresh was allowed after logout: %v", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not finish after peer sign-out")
	}
	current := oauthTestSavedConnection(t, first, connection.ID)
	if len(current.OAuthCache) != 0 || current.OAuthAccountID != "" || current.OAuthGeneration == frozen.OAuthGeneration {
		t.Fatal("in-flight refresh resurrected signed-out credentials")
	}
	if token, err := provider(context.Background()); token != "" || !errors.Is(err, errOAuthSignInRequired) {
		t.Fatal("frozen run continued using a signed-out connection")
	}
}

func TestOAuthSDKFailureMessagesNeverExposeSecrets(t *testing.T) {
	s, _ := workspaceTestService(t)
	connection := oauthTestConnection(t, s)
	secret := "refresh_token=SUPER-SECRET-SDK-RESPONSE"
	s.oauthSignIn = func(context.Context, azureauth.Settings) (azureauth.Session, error) {
		return azureauth.Session{}, errors.New(secret)
	}
	state, err := s.SignInLLMConnection(connection.ID)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("raw SDK sign-in failure was exposed")
	}
	encoded, marshalErr := json.Marshal(state)
	if marshalErr != nil || strings.Contains(string(encoded), secret) {
		t.Fatal("SDK failure contaminated public state")
	}
	oauthTestSignIn(t, s, connection)
	before := oauthTestSavedConnection(t, s, connection.ID)
	s.oauthAcquire = func(context.Context, azureauth.Settings, azureauth.Session) (string, azureauth.Session, error) {
		return "", azureauth.Session{}, errors.New(secret)
	}
	token, err := s.oauthTokenProvider(before)(context.Background())
	if err == nil || token != "" || strings.Contains(err.Error(), secret) {
		t.Fatal("raw SDK refresh failure was exposed")
	}
	after := oauthTestSavedConnection(t, s, connection.ID)
	if string(after.OAuthCache) != string(before.OAuthCache) || after.OAuthGeneration != before.OAuthGeneration {
		t.Fatal("failed refresh changed the saved credentials")
	}
}
