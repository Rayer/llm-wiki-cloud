package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func isControlPlaneCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "auth", "projects", "bind", "binding":
		return true
	default:
		return false
	}
}

func runControlPlaneCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: lwc-sync auth|projects|bind|binding")
	}
	switch args[0] {
	case "auth":
		if len(args) < 2 {
			return errors.New("usage: lwc-sync auth login|status|refresh|logout")
		}
		switch args[1] {
		case "login":
			return runAuthLogin(args[2:])
		case "status":
			return runAuthStatus(args[2:])
		case "refresh":
			return runAuthRefresh(args[2:])
		case "logout":
			return runAuthLogout(args[2:])
		default:
			return errors.New("usage: lwc-sync auth login|status|refresh|logout")
		}
	case "projects":
		return runProjects(args[1:])
	case "bind":
		return runBind(args[1:])
	case "binding":
		if len(args) < 2 {
			return errors.New("usage: lwc-sync binding list|reauthorize|revoke")
		}
		switch args[1] {
		case "list":
			return runBindingList(args[2:])
		case "reauthorize":
			return runBindingReauthorize(args[2:])
		case "revoke":
			return runBindingRevoke(args[2:])
		default:
			return errors.New("usage: lwc-sync binding list|reauthorize|revoke")
		}
	default:
		return errors.New("usage: lwc-sync auth|projects|bind|binding")
	}
}

func runAuthLogin(args []string) error {
	flags := flag.NewFlagSet("auth login", flag.ContinueOnError)
	host := flags.String("host", "", "Auth/control-plane origin (HTTPS; localhost HTTP is allowed)")
	noBrowser := flags.Bool("no-browser", false, "print the pairing URL without opening a browser")
	clientName := flags.String("client-name", "lwc-sync CLI", "name shown in account settings")
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, err := defaultLocalAuthStore()
	if err != nil {
		return err
	}
	origin, err := selectAuthHost(store, *host)
	if err != nil {
		return err
	}
	client, err := newCLIAPIClient(origin, nil)
	if err != nil {
		return err
	}
	var pairing struct {
		PairingID           string    `json:"pairing_id"`
		UserCode            string    `json:"user_code"`
		VerificationURL     string    `json:"verification_url"`
		PollingSecret       string    `json:"polling_secret"`
		ExpiresAt           time.Time `json:"expires_at"`
		PollIntervalSeconds int       `json:"poll_interval_seconds"`
	}
	if err := client.request(context.Background(), http.MethodPost, "/api/v1/auth/cli/pairing/start", map[string]string{"client_name": *clientName}, &pairing); err != nil {
		return err
	}
	if pairing.PairingID == "" || pairing.UserCode == "" || pairing.PollingSecret == "" || pairing.VerificationURL == "" {
		return errors.New("Auth service returned an incomplete pairing request")
	}
	fmt.Printf("請在瀏覽器登入並核准此 CLI：%s\n驗證碼：%s\n有效期限：%s\n", pairing.VerificationURL, pairing.UserCode, pairing.ExpiresAt.Local().Format(time.RFC1123))
	if !*noBrowser {
		if openBrowser(pairing.VerificationURL) {
			fmt.Println("已嘗試開啟瀏覽器；若未開啟，請手動使用上方網址。")
		}
	}
	interval := time.Duration(pairing.PollIntervalSeconds) * time.Second
	if interval < time.Second || interval > 30*time.Second {
		interval = 3 * time.Second
	}
	for {
		if !pairing.ExpiresAt.IsZero() && time.Now().After(pairing.ExpiresAt) {
			return errors.New("配對已過期，請重新執行 `lwc-sync auth login`")
		}
		time.Sleep(interval)
		response, err := client.do(context.Background(), http.MethodPost, "/api/v1/auth/cli/pairing/poll", map[string]string{"pairing_id": pairing.PairingID, "polling_secret": pairing.PollingSecret}, false)
		if err != nil {
			return err
		}
		switch response.status {
		case http.StatusAccepted:
			continue
		case http.StatusOK:
			var result struct {
				Status      string `json:"status"`
				Credentials struct {
					AccessToken  string `json:"access_token"`
					RefreshToken string `json:"refresh_token"`
					SessionID    string `json:"session_id"`
					UserID       string `json:"user_id"`
					Role         string `json:"role"`
					AuthVersion  int64  `json:"auth_version"`
				} `json:"credentials"`
			}
			if err := json.Unmarshal(response.body, &result); err != nil || result.Status != "redeemed" || result.Credentials.AccessToken == "" || result.Credentials.RefreshToken == "" || result.Credentials.SessionID == "" {
				return errors.New("Auth service returned an invalid CLI grant")
			}
			credentials := cliLocalCredentials{
				AuthHost: origin, AccessToken: result.Credentials.AccessToken, RefreshToken: result.Credentials.RefreshToken,
				SessionID: result.Credentials.SessionID, UserID: result.Credentials.UserID, Role: result.Credentials.Role,
				AuthVersion: result.Credentials.AuthVersion,
			}
			if err := store.withLock(func() error {
				if err := store.saveConfig(cliLocalConfig{AuthHost: origin}); err != nil {
					return err
				}
				return store.saveCredentials(credentials)
			}); err != nil {
				return errors.New("could not safely save local CLI credentials")
			}
			fmt.Println("CLI 登入完成；憑證保存在本機私有設定目錄。")
			return nil
		case http.StatusForbidden:
			return errors.New("配對已被拒絕")
		case http.StatusGone:
			return errors.New("配對已過期，請重新執行 `lwc-sync auth login`")
		case http.StatusUnauthorized, http.StatusConflict:
			return errors.New("配對無效或已使用，請重新執行 `lwc-sync auth login`")
		default:
			return cliAPIStatusError{status: response.status}
		}
	}
}

func runAuthStatus(args []string) error {
	flags := flag.NewFlagSet("auth status", flag.ContinueOnError)
	host := flags.String("host", "", "Auth/control-plane origin")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return withStoredCredentials(*host, func(client *cliAPIClient, credentials *cliLocalCredentials) error {
		var status struct {
			UserID    string `json:"user_id"`
			SessionID string `json:"session_id"`
			Role      string `json:"role"`
		}
		if err := client.request(context.Background(), http.MethodGet, "/api/v1/auth/cli/status", nil, &status); err != nil {
			return err
		}
		if status.UserID == "" {
			status.UserID = credentials.UserID
		}
		fmt.Printf("已登入\n使用者：%s\nSession：%s\n角色：%s\nAuth host：%s\n", status.UserID, status.SessionID, status.Role, client.origin)
		return nil
	})
}

func runAuthRefresh(args []string) error {
	flags := flag.NewFlagSet("auth refresh", flag.ContinueOnError)
	host := flags.String("host", "", "Auth/control-plane origin")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return withStoredCredentials(*host, func(client *cliAPIClient, _ *cliLocalCredentials) error {
		if err := client.refresh(context.Background()); err != nil {
			return err
		}
		fmt.Println("CLI 憑證已更新。")
		return nil
	})
}

func runAuthLogout(args []string) error {
	flags := flag.NewFlagSet("auth logout", flag.ContinueOnError)
	host := flags.String("host", "", "Auth/control-plane origin")
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, err := defaultLocalAuthStore()
	if err != nil {
		return err
	}
	origin, err := selectAuthHost(store, *host)
	if err != nil {
		return err
	}
	serverRevoked := false
	var networkOrServerErr error
	err = store.withLock(func() error {
		credentials, err := store.loadCredentials()
		if err != nil {
			return err
		}
		if credentials.AuthHost != origin {
			return errors.New("local credentials belong to a different auth/control-plane host")
		}
		client, err := newCLIAPIClient(origin, &credentials)
		if err != nil {
			return err
		}
		response, requestErr := client.do(context.Background(), http.MethodPost, "/api/v1/auth/cli/logout", map[string]string{"refresh_token": credentials.RefreshToken}, false)
		if requestErr != nil {
			networkOrServerErr = requestErr
		} else if response.status >= 200 && response.status < 300 {
			serverRevoked = true
		} else if response.status != http.StatusUnauthorized && response.status != http.StatusGone && response.status != http.StatusNotFound {
			networkOrServerErr = cliAPIStatusError{status: response.status}
		}
		return store.clearCredentials()
	})
	if err != nil {
		if errors.Is(err, errCLILocalAuthNotFound) {
			fmt.Println("本機沒有 CLI 登入憑證。")
			return nil
		}
		return err
	}
	if serverRevoked {
		fmt.Println("已向伺服器撤銷 CLI session，並清除本機憑證。")
		return nil
	}
	if networkOrServerErr != nil {
		fmt.Printf("本機憑證已清除；伺服器尚未確認撤銷 session（%v）。\n", networkOrServerErr)
		return nil
	}
	fmt.Println("本機憑證已清除；伺服器 session 已無效或先前已撤銷。")
	return nil
}

func runProjects(args []string) error {
	flags := flag.NewFlagSet("projects", flag.ContinueOnError)
	host := flags.String("host", "", "Auth/control-plane origin")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return withStoredCredentials(*host, func(client *cliAPIClient, _ *cliLocalCredentials) error {
		projects := make([]cliProject, 0)
		next := ""
		for {
			query := url.Values{"page_size": []string{"100"}}
			if next != "" {
				query.Set("page_token", next)
			}
			var page struct {
				Projects      []cliProject `json:"projects"`
				NextPageToken string       `json:"next_page_token"`
			}
			if err := client.request(context.Background(), http.MethodGet, "/api/v1/auth/cli/projects?"+query.Encode(), nil, &page); err != nil {
				return err
			}
			projects = append(projects, page.Projects...)
			if page.NextPageToken == "" {
				break
			}
			next = page.NextPageToken
		}
		if *jsonOutput {
			return printJSON(os.Stdout, map[string]interface{}{"projects": projects})
		}
		if len(projects) == 0 {
			fmt.Println("沒有可用的 Project。")
			return nil
		}
		fmt.Println("ID\t名稱")
		for _, project := range projects {
			fmt.Printf("%s\t%s\n", project.ID, project.Name)
		}
		return nil
	})
}

type cliProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func runBind(args []string) error {
	flags := flag.NewFlagSet("bind", flag.ContinueOnError)
	host := flags.String("host", "", "Auth/control-plane origin")
	vault := flags.String("vault", "", "path to the local wiki vault")
	projectID := flags.String("project-id", "", "Cloud Project ID to authorize")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*vault) == "" || strings.TrimSpace(*projectID) == "" {
		return errors.New("usage: lwc-sync bind --vault PATH --project-id ID")
	}
	store, err := defaultLocalAuthStore()
	if err != nil {
		return err
	}
	origin, err := selectAuthHost(store, *host)
	if err != nil {
		return err
	}
	binding, err := prepareVaultBinding(*vault, origin, *projectID)
	if err != nil {
		return err
	}
	return withStoredCredentials(origin, func(client *cliAPIClient, _ *cliLocalCredentials) error {
		var created struct {
			ID string `json:"binding_id"`
		}
		body := map[string]string{"project_id": binding.ProjectID, "wiki_id": binding.WikiID, "host": binding.Host}
		if err := client.request(context.Background(), http.MethodPost, "/api/v1/auth/cli/bindings", body, &created); err != nil {
			return err
		}
		if created.ID == "" {
			return errors.New("Auth service returned an invalid sync binding")
		}
		if err := saveVaultBindingID(*vault, binding, created.ID); err != nil {
			return errors.New("server authorized the binding, but the vault metadata could not be updated")
		}
		fmt.Printf("已授權 Project %s；本次未傳輸 wiki 資料。\n", binding.ProjectID)
		return nil
	})
}

func runBindingList(args []string) error {
	flags := flag.NewFlagSet("binding list", flag.ContinueOnError)
	host := flags.String("host", "", "Auth/control-plane origin")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return withStoredCredentials(*host, func(client *cliAPIClient, _ *cliLocalCredentials) error {
		var result struct {
			Bindings []authSyncBinding `json:"bindings"`
		}
		if err := client.request(context.Background(), http.MethodGet, "/api/v1/auth/cli/bindings", nil, &result); err != nil {
			return err
		}
		if *jsonOutput {
			return printJSON(os.Stdout, result)
		}
		if len(result.Bindings) == 0 {
			fmt.Println("沒有 sync binding。")
			return nil
		}
		fmt.Println("Project ID\tWiki ID\tBinding ID\t狀態")
		for _, binding := range result.Bindings {
			fmt.Printf("%s\t%s\t%s\t%s\n", binding.ProjectID, binding.WikiID, binding.ID, binding.Status)
		}
		return nil
	})
}

type authSyncBinding struct {
	ID        string `json:"binding_id"`
	Host      string `json:"host"`
	ProjectID string `json:"project_id"`
	WikiID    string `json:"wiki_id"`
	Status    string `json:"status"`
}

func runBindingReauthorize(args []string) error {
	flags := flag.NewFlagSet("binding reauthorize", flag.ContinueOnError)
	host := flags.String("host", "", "Auth/control-plane origin")
	vault := flags.String("vault", "", "path to the local wiki vault")
	projectID := flags.String("project-id", "", "Cloud Project ID to reauthorize")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*vault) == "" || strings.TrimSpace(*projectID) == "" {
		return errors.New("usage: lwc-sync binding reauthorize --vault PATH --project-id ID")
	}
	binding, err := loadVaultBinding(*vault)
	if err != nil {
		return err
	}
	if binding.BindingID == "" || binding.ProjectID != *projectID {
		return errors.New("vault has no matching binding to reauthorize")
	}
	requestedHost := *host
	if requestedHost != "" {
		requestedHost, err = normalizeAuthOrigin(requestedHost)
		if err != nil || requestedHost != binding.Host {
			return errors.New("vault binding belongs to a different auth/control-plane host")
		}
	}
	return withStoredCredentials(binding.Host, func(client *cliAPIClient, _ *cliLocalCredentials) error {
		var currentBindings struct {
			Bindings []authSyncBinding `json:"bindings"`
		}
		if err := client.request(context.Background(), http.MethodGet, "/api/v1/auth/cli/bindings", nil, &currentBindings); err != nil {
			return err
		}
		for _, current := range currentBindings.Bindings {
			if current.ProjectID != binding.ProjectID {
				continue
			}
			if current.ID != binding.BindingID {
				if current.Status != "active" || current.WikiID != binding.WikiID || current.Host != binding.Host {
					return errors.New("server binding changed; verify the wiki and Project in account settings")
				}
				if err := saveVaultBindingID(*vault, binding, current.ID); err != nil {
					return errors.New("could not update the vault to the explicitly reauthorized server binding")
				}
				fmt.Printf("已同步 Web 重新授權的 Project %s binding；本次未傳輸 wiki 資料。\n", binding.ProjectID)
				return nil
			}
			break
		}
		var replaced struct {
			ID string `json:"binding_id"`
		}
		body := map[string]string{"binding_id": binding.BindingID, "wiki_id": binding.WikiID, "host": binding.Host}
		route := "/api/v1/auth/cli/bindings/" + url.PathEscape(binding.ProjectID) + "/reauthorize"
		if err := client.request(context.Background(), http.MethodPost, route, body, &replaced); err != nil {
			return err
		}
		if replaced.ID == "" {
			return errors.New("Auth service returned an invalid sync binding")
		}
		if err := saveVaultBindingID(*vault, binding, replaced.ID); err != nil {
			return errors.New("server reauthorized the binding, but the vault metadata could not be updated")
		}
		fmt.Printf("Project %s 的 binding 已明確重新授權。\n", binding.ProjectID)
		return nil
	})
}

func runBindingRevoke(args []string) error {
	flags := flag.NewFlagSet("binding revoke", flag.ContinueOnError)
	host := flags.String("host", "", "Auth/control-plane origin")
	projectID := flags.String("project-id", "", "Cloud Project ID")
	bindingID := flags.String("binding-id", "", "binding ID to revoke")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !validVaultID(*projectID) || !validVaultID(*bindingID) {
		return errors.New("usage: lwc-sync binding revoke --project-id ID --binding-id ID")
	}
	return withStoredCredentials(*host, func(client *cliAPIClient, _ *cliLocalCredentials) error {
		route := "/api/v1/auth/cli/bindings/" + url.PathEscape(*projectID) + "/" + url.PathEscape(*bindingID) + "/revoke"
		if err := client.request(context.Background(), http.MethodPost, route, nil, nil); err != nil {
			return err
		}
		fmt.Println("sync binding 已撤銷；CLI session 與 wiki 資料未變更。")
		return nil
	})
}

func selectAuthHost(store *localAuthStore, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		requested = strings.TrimSpace(os.Getenv("LWC_SYNC_AUTH_HOST"))
	}
	if requested != "" {
		var err error
		requested, err = normalizeAuthOrigin(requested)
		if err != nil {
			return "", errors.New("auth/control-plane host must be a trusted HTTPS origin (or local HTTP origin)")
		}
	}
	var selected string
	err := store.withLock(func() error {
		config, err := store.loadConfig()
		if errors.Is(err, os.ErrNotExist) {
			if requested == "" {
				return errors.New("set LWC_SYNC_AUTH_HOST or pass --host with the Auth/control-plane origin")
			}
			selected = requested
			return store.saveConfig(cliLocalConfig{AuthHost: selected})
		}
		if err != nil {
			return err
		}
		selected = config.AuthHost
		if requested == "" {
			return nil
		}
		if requested != config.AuthHost {
			if _, statErr := os.Lstat(store.credentialsPath()); statErr == nil {
				return errors.New("existing CLI credentials are bound to the configured auth/control-plane host; log out first")
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return statErr
			}
			selected = requested
			return store.saveConfig(cliLocalConfig{AuthHost: selected})
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return selected, nil
}

func withStoredCredentials(requestedHost string, fn func(*cliAPIClient, *cliLocalCredentials) error) error {
	store, err := defaultLocalAuthStore()
	if err != nil {
		return err
	}
	origin, err := selectAuthHost(store, requestedHost)
	if err != nil {
		return err
	}
	return store.withLock(func() error {
		config, err := store.loadConfig()
		if err != nil || config.AuthHost != origin {
			return errors.New("configured auth/control-plane host changed; retry the command")
		}
		credentials, err := store.loadCredentials()
		if err != nil {
			return err
		}
		if credentials.AuthHost != origin {
			return errors.New("local credentials belong to a different auth/control-plane host")
		}
		client, err := newCLIAPIClient(origin, &credentials)
		if err != nil {
			return err
		}
		before := credentials
		commandErr := fn(client, &credentials)
		if credentials != before {
			if err := store.saveCredentials(credentials); err != nil {
				return errors.New("could not safely save rotated local CLI credentials")
			}
		}
		if errors.Is(commandErr, errCLIReauthenticationRequired) {
			if err := store.clearCredentials(); err != nil {
				return errors.New("CLI session expired and local credentials could not be cleared")
			}
		}
		return commandErr
	})
}

func printJSON(out *os.File, value interface{}) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func openBrowser(target string) bool {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Run() == nil
}
