package engine

import (
        "context"
        "crypto/rand"
        "encoding/hex"
        "encoding/json"
        "fmt"
        "strings"
        "time"

        "github.com/umbraforge/desktop/internal/pkg/cfemail"
        "github.com/umbraforge/desktop/internal/pkg/grokregister"
        "github.com/umbraforge/desktop/internal/store"
)

type EduWorker struct {
        ID            string `json:"id"`
        Name          string `json:"name,omitempty"`
        Domain        string `json:"domain"`
        WorkerURL     string `json:"worker_url"`
        ApiKey        string `json:"api_key,omitempty"`
        CFAccountID   string `json:"cf_account_id,omitempty"`
        CFApiToken    string `json:"cf_api_token,omitempty"`
        KVNamespaceID string `json:"kv_namespace_id,omitempty"`
        Enabled         bool   `json:"enabled"`
        UseEduSubdomain bool   `json:"use_edu_subdomain,omitempty"`
        Notes           string `json:"notes,omitempty"`
        CreatedAt       string `json:"created_at,omitempty"`
        UpdatedAt       string `json:"updated_at,omitempty"`
}

type EduStoreDoc struct {
        Workers []EduWorker `json:"workers"`
}

// CFAccount is a saved Cloudflare account credential for EDU provisioning.
type CFAccount struct {
        ID          string `json:"id"`
        Name        string `json:"name,omitempty"`
        AccountID   string `json:"cf_account_id"`
        APIToken    string `json:"cf_api_token,omitempty"`
        CallbackURL string `json:"callback_url,omitempty"`
        Notes       string `json:"notes,omitempty"`
        IsDefault   bool   `json:"is_default,omitempty"`
        CreatedAt   string `json:"created_at,omitempty"`
        UpdatedAt   string `json:"updated_at,omitempty"`
        LastUsedAt  string `json:"last_used_at,omitempty"`
}

type CFAccountStoreDoc struct {
        Accounts      []CFAccount `json:"accounts"`
        ActiveID      string      `json:"active_id,omitempty"`
}

const KeyEduCFAccounts = "edu_cf_accounts"

type EduService struct {
        store *store.JSONStore
}

func NewEduService(st *store.JSONStore) *EduService {
        return &EduService{store: st}
}

func (s *EduService) List(ctx context.Context) (EduStoreDoc, error) {
        workers, err := s.ListRaw(ctx)
        if err != nil {
                return EduStoreDoc{}, err
        }
        out := EduStoreDoc{Workers: make([]EduWorker, len(workers))}
        copy(out.Workers, workers)
        for i := range out.Workers {
                if out.Workers[i].ApiKey != "" {
                        out.Workers[i].ApiKey = maskSecret(out.Workers[i].ApiKey)
                }
                if out.Workers[i].CFApiToken != "" {
                        out.Workers[i].CFApiToken = maskSecret(out.Workers[i].CFApiToken)
                }
        }
        return out, nil
}

func (s *EduService) ListRaw(ctx context.Context) ([]EduWorker, error) {
        raw, err := s.store.GetValue(ctx, KeyEdu)
        if err != nil {
                return nil, err
        }
        var st EduStoreDoc
        if strings.TrimSpace(raw) != "" {
                _ = json.Unmarshal([]byte(raw), &st)
        }
        if st.Workers == nil {
                st.Workers = []EduWorker{}
        }
        return st.Workers, nil
}

func (s *EduService) Upsert(ctx context.Context, in EduWorker) (EduWorker, error) {
        workers, err := s.ListRaw(ctx)
        if err != nil {
                return in, err
        }
        now := time.Now().UTC().Format(time.RFC3339)
        if strings.TrimSpace(in.ID) == "" {
                in.ID = randID()
                in.CreatedAt = now
                // New workers must be usable by register engine (Go bool zero is false).
                in.Enabled = true
        }
        in.UpdatedAt = now
        for i, w := range workers {
                if w.ID == in.ID {
                        if strings.Contains(in.ApiKey, "***") || in.ApiKey == "" {
                                in.ApiKey = w.ApiKey
                        }
                        if strings.Contains(in.CFApiToken, "***") || in.CFApiToken == "" {
                                in.CFApiToken = w.CFApiToken
                        }
                        workers[i] = in
                        return in, s.save(ctx, workers)
                }
        }
        workers = append(workers, in)
        return in, s.save(ctx, workers)
}

func (s *EduService) Delete(ctx context.Context, id string) error {
        workers, err := s.ListRaw(ctx)
        if err != nil {
                return err
        }
        out := make([]EduWorker, 0, len(workers))
        for _, w := range workers {
                if w.ID != id {
                        out = append(out, w)
                }
        }
        return s.save(ctx, out)
}

func (s *EduService) ReplaceAll(ctx context.Context, workers []EduWorker) (EduStoreDoc, error) {
        if err := s.save(ctx, workers); err != nil {
                return EduStoreDoc{}, err
        }
        return s.List(ctx)
}

func (s *EduService) save(ctx context.Context, workers []EduWorker) error {
        b, _ := json.Marshal(EduStoreDoc{Workers: workers})
        return s.store.Set(ctx, KeyEdu, string(b))
}

func (s *EduService) ListCFZones(ctx context.Context, accountID, apiToken string, probe bool) ([]cfemail.ZoneInfo, error) {
        return cfemail.ListZonesWithEmailStatus(accountID, apiToken, probe)
}


// SelectByIDs returns enabled workers. Empty ids → all enabled workers.
// Non-empty ids → matching enabled workers in the given order.
func (s *EduService) SelectByIDs(ctx context.Context, ids []string) ([]EduWorker, error) {
        workers, err := s.ListRaw(ctx)
        if err != nil {
                return nil, err
        }
        byID := make(map[string]EduWorker, len(workers))
        for _, w := range workers {
                byID[w.ID] = w
        }
        out := make([]EduWorker, 0, len(workers))
        if len(ids) == 0 {
                for _, w := range workers {
                        if w.Enabled {
                                out = append(out, w)
                        }
                }
                return out, nil
        }
        seen := map[string]bool{}
        for _, id := range ids {
                id = strings.TrimSpace(id)
                if id == "" || seen[id] {
                        continue
                }
                seen[id] = true
                w, ok := byID[id]
                if !ok || !w.Enabled {
                        continue
                }
                out = append(out, w)
        }
        return out, nil
}

func (s *EduService) GenerateAddresses(domain string, n int, useEduSubdomain bool) []string {
        if n <= 0 {
                n = 5
        }
        if n > 100 {
                n = 100
        }
        return cfemail.GenerateAddresses(domain, n, useEduSubdomain)
}

func (s *EduService) Provision(ctx context.Context, req cfemail.ProvisionRequest) (cfemail.ProvisionResult, error) {
        res := cfemail.Provision(req)
        if res.Entry != nil {
                _, _ = s.Upsert(ctx, EduWorker{
                        Name:          firstNonEmpty(req.Name, res.Entry.Name, res.Entry.Domain),
                        Domain:        res.Entry.Domain,
                        WorkerURL:     res.Entry.WorkerURL,
                        ApiKey:        res.Entry.ApiKey,
                        CFAccountID:   firstNonEmpty(res.Entry.CFAccountID, req.AccountID),
                        CFApiToken:    firstNonEmpty(res.Entry.CFApiToken, req.APIToken),
                        KVNamespaceID: res.Entry.KVNamespaceID,
                        Enabled:       true,
                })
        }
        return res, nil
}

func (s *EduService) ProvisionBatch(ctx context.Context, accountID, apiToken, callbackURL, namePrefix string, domains []string) map[string]any {
        results := make([]cfemail.ProvisionResult, 0, len(domains))
        okN, failN := 0, 0
        for i, d := range domains {
                d = strings.TrimSpace(d)
                if d == "" {
                        continue
                }
                name := strings.TrimSpace(namePrefix)
                if name != "" {
                        name = name + "-" + d
                }
                res, _ := s.Provision(ctx, cfemail.ProvisionRequest{
                        Name:        name,
                        Domain:      d,
                        AccountID:   accountID,
                        APIToken:    apiToken,
                        CallbackURL: callbackURL,
                })
                results = append(results, res)
                if res.OK {
                        okN++
                } else {
                        failN++
                }
                _ = i
        }
        return map[string]any{"ok": okN, "fail": failN, "results": results}
}

func (s *EduService) ReceiveCallback(ctx context.Context, email, code, body, key string) bool {
        email = strings.ToLower(strings.TrimSpace(email))
        at := strings.LastIndex(email, "@")
        if at <= 0 {
                return false
        }
        domain := email[at+1:]
        workers, err := s.ListRaw(ctx)
        if err != nil {
                return false
        }
        for _, w := range workers {
                if !strings.EqualFold(strings.TrimSpace(w.Domain), domain) {
                        continue
                }
                if strings.TrimSpace(w.ApiKey) == "" || strings.TrimSpace(w.ApiKey) != strings.TrimSpace(key) {
                        continue
                }
                return grokregister.StorePushedOTP(email, code, body)
        }
        return false
}


// ---------- CF multi-account credentials (permanent local store) ----------

func (s *EduService) loadCFAccounts(ctx context.Context) (CFAccountStoreDoc, error) {
        raw, err := s.store.GetValue(ctx, KeyEduCFAccounts)
        if err != nil {
                return CFAccountStoreDoc{}, err
        }
        var st CFAccountStoreDoc
        if strings.TrimSpace(raw) != "" {
                _ = json.Unmarshal([]byte(raw), &st)
        }
        if st.Accounts == nil {
                st.Accounts = []CFAccount{}
        }
        return st, nil
}

func (s *EduService) saveCFAccounts(ctx context.Context, st CFAccountStoreDoc) error {
        b, _ := json.Marshal(st)
        return s.store.Set(ctx, KeyEduCFAccounts, string(b))
}

// ListCFAccounts returns accounts with tokens masked.
func (s *EduService) ListCFAccounts(ctx context.Context) (CFAccountStoreDoc, error) {
        st, err := s.loadCFAccounts(ctx)
        if err != nil {
                return st, err
        }
        out := CFAccountStoreDoc{ActiveID: st.ActiveID, Accounts: make([]CFAccount, len(st.Accounts))}
        copy(out.Accounts, st.Accounts)
        for i := range out.Accounts {
                if out.Accounts[i].APIToken != "" {
                        out.Accounts[i].APIToken = maskSecret(out.Accounts[i].APIToken)
                }
        }
        return out, nil
}

// GetCFAccountRaw returns full secrets for a single account (for zones/provision).
func (s *EduService) GetCFAccountRaw(ctx context.Context, id string) (CFAccount, bool, error) {
        st, err := s.loadCFAccounts(ctx)
        if err != nil {
                return CFAccount{}, false, err
        }
        id = strings.TrimSpace(id)
        for _, a := range st.Accounts {
                if a.ID == id {
                        return a, true, nil
                }
        }
        // fallback: active
        if id == "" && st.ActiveID != "" {
                for _, a := range st.Accounts {
                        if a.ID == st.ActiveID {
                                return a, true, nil
                        }
                }
        }
        if id == "" && len(st.Accounts) > 0 {
                return st.Accounts[0], true, nil
        }
        return CFAccount{}, false, nil
}

// UpsertCFAccount creates or updates a CF account. Masked token "***" keeps previous.
func (s *EduService) UpsertCFAccount(ctx context.Context, in CFAccount) (CFAccount, error) {
        st, err := s.loadCFAccounts(ctx)
        if err != nil {
                return in, err
        }
        now := time.Now().UTC().Format(time.RFC3339)
        in.AccountID = strings.TrimSpace(in.AccountID)
        in.APIToken = strings.TrimSpace(in.APIToken)
        in.Name = strings.TrimSpace(in.Name)
        in.CallbackURL = strings.TrimSpace(in.CallbackURL)
        if in.AccountID == "" {
                return in, fmt.Errorf("cf_account_id 不能为空")
        }
        if strings.TrimSpace(in.ID) == "" {
                if in.APIToken == "" || strings.Contains(in.APIToken, "***") {
                        return in, fmt.Errorf("新建账号需要完整 API Token")
                }
                in.ID = randID()
                in.CreatedAt = now
                if in.Name == "" {
                        // short label from account id
                        if len(in.AccountID) > 8 {
                                in.Name = "CF " + in.AccountID[:8]
                        } else {
                                in.Name = "CF " + in.AccountID
                        }
                }
                // first account becomes default/active
                if len(st.Accounts) == 0 {
                        in.IsDefault = true
                        st.ActiveID = in.ID
                }
                in.UpdatedAt = now
                st.Accounts = append(st.Accounts, in)
        } else {
                found := false
                for i, a := range st.Accounts {
                        if a.ID != in.ID {
                                continue
                        }
                        found = true
                        if strings.Contains(in.APIToken, "***") || in.APIToken == "" {
                                in.APIToken = a.APIToken
                        }
                        if in.CreatedAt == "" {
                                in.CreatedAt = a.CreatedAt
                        }
                        in.UpdatedAt = now
                        // is_default exclusive
                        if in.IsDefault {
                                for j := range st.Accounts {
                                        st.Accounts[j].IsDefault = st.Accounts[j].ID == in.ID
                                }
                                st.ActiveID = in.ID
                        } else {
                                in.IsDefault = a.IsDefault
                        }
                        st.Accounts[i] = in
                        break
                }
                if !found {
                        return in, fmt.Errorf("账号不存在: %s", in.ID)
                }
        }
        if in.IsDefault {
                for i := range st.Accounts {
                        st.Accounts[i].IsDefault = st.Accounts[i].ID == in.ID
                }
                st.ActiveID = in.ID
        }
        if err := s.saveCFAccounts(ctx, st); err != nil {
                return in, err
        }
        // return masked
        if in.APIToken != "" {
                in.APIToken = maskSecret(in.APIToken)
        }
        return in, nil
}

func (s *EduService) DeleteCFAccount(ctx context.Context, id string) error {
        st, err := s.loadCFAccounts(ctx)
        if err != nil {
                return err
        }
        out := make([]CFAccount, 0, len(st.Accounts))
        for _, a := range st.Accounts {
                if a.ID != id {
                        out = append(out, a)
                }
        }
        st.Accounts = out
        if st.ActiveID == id {
                st.ActiveID = ""
                if len(out) > 0 {
                        st.ActiveID = out[0].ID
                        out[0].IsDefault = true
                        st.Accounts = out
                }
        }
        return s.saveCFAccounts(ctx, st)
}

func (s *EduService) SetActiveCFAccount(ctx context.Context, id string) (CFAccountStoreDoc, error) {
        st, err := s.loadCFAccounts(ctx)
        if err != nil {
                return st, err
        }
        found := false
        for i := range st.Accounts {
                if st.Accounts[i].ID == id {
                        found = true
                        st.Accounts[i].IsDefault = true
                        st.Accounts[i].LastUsedAt = time.Now().UTC().Format(time.RFC3339)
                } else {
                        st.Accounts[i].IsDefault = false
                }
        }
        if !found {
                return st, fmt.Errorf("账号不存在: %s", id)
        }
        st.ActiveID = id
        if err := s.saveCFAccounts(ctx, st); err != nil {
                return st, err
        }
        return s.ListCFAccounts(ctx)
}

func (s *EduService) TouchCFAccountUsed(ctx context.Context, id string) {
        st, err := s.loadCFAccounts(ctx)
        if err != nil {
                return
        }
        now := time.Now().UTC().Format(time.RFC3339)
        for i := range st.Accounts {
                if st.Accounts[i].ID == id {
                        st.Accounts[i].LastUsedAt = now
                        _ = s.saveCFAccounts(ctx, st)
                        return
                }
        }
}


// ImportOpen imports one already-open CF domain into the local EDU pool (no full re-provision).
func (s *EduService) ImportOpen(ctx context.Context, req cfemail.ImportOpenRequest) (cfemail.ImportOpenResult, error) {
	res := cfemail.ImportOpen(req)
	if res.OK && res.Entry != nil {
		w, err := s.Upsert(ctx, EduWorker{
			Name:          firstNonEmpty(req.Name, res.Entry.Name, res.Entry.Domain),
			Domain:        res.Entry.Domain,
			WorkerURL:     res.Entry.WorkerURL,
			ApiKey:        res.Entry.ApiKey,
			CFAccountID:   firstNonEmpty(res.Entry.CFAccountID, req.AccountID),
			CFApiToken:    firstNonEmpty(res.Entry.CFApiToken, req.APIToken),
			KVNamespaceID: res.Entry.KVNamespaceID,
			Enabled:       true,
		})
		if err != nil {
			res.OK = false
			res.Error = "写入 EDU 池失败: " + err.Error()
			return res, nil
		}
		// reflect assigned id
		res.Entry.ID = w.ID
		res.Entry.Name = w.Name
	}
	return res, nil
}

// ImportOpenBatch imports multiple CF-open domains.
func (s *EduService) ImportOpenBatch(ctx context.Context, accountID, apiToken, callbackURL, namePrefix string, domains []string) map[string]any {
	results := make([]cfemail.ImportOpenResult, 0, len(domains))
	okN, failN := 0, 0
	for _, d := range domains {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		name := strings.TrimSpace(namePrefix)
		if name != "" {
			name = name + "-" + d
		}
		res, _ := s.ImportOpen(ctx, cfemail.ImportOpenRequest{
			Name:        name,
			Domain:      d,
			AccountID:   accountID,
			APIToken:    apiToken,
			CallbackURL: callbackURL,
		})
		results = append(results, res)
		if res.OK {
			okN++
		} else {
			failN++
		}
	}
	return map[string]any{"ok": okN, "fail": failN, "results": results}
}

func (s *EduService) CallbackURLHint(localBase string) string {
        // public path used by CF Email Worker
        return strings.TrimRight(localBase, "/") + "/api/v1/admin/edu-email/callback"
}

func randID() string {
        var b [8]byte
        _, _ = rand.Read(b[:])
        return hex.EncodeToString(b[:])
}

func firstNonEmpty(vs ...string) string {
        for _, v := range vs {
                if strings.TrimSpace(v) != "" {
                        return v
                }
        }
        return ""
}
