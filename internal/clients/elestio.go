package clients

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ElestioClient struct {
	http *HTTPClient
}

type ElestioConfig struct {
	BaseURL string
}

type TemplateSpec struct {
	TemplateID       int                    `json:"template_id"`
	TemplateName     string                 `json:"template_name"`
	Category         string                 `json:"category"`
	Description      string                 `json:"description"`
	TemplateVersion  string                 `json:"template_version"`
	BootstrapCommand string                 `json:"bootstrap_command"`
	Ports            []int                  `json:"ports"`
	RawTemplate      map[string]interface{} `json:"raw_template"`
}

type TestBYOVMRequest struct {
	JWT               string
	UserName          string
	Port              int
	IPv4              string
	PrivateKeyContent string
	Passphrase        string
}

type CreateBYOVMServiceRequest struct {
	JWT                string
	TemplateID         int
	TemplateVersion    string
	ServerName         string
	AdminEmail         string
	ProjectID          string
	Datacenter         string
	AppID              string
	SupportPlan        string
	DeploymentType     string
	ServiceType        string
	ProviderName       string
	ProviderCustomName string
	PublicIPv4         string
	IPv6               string
	PrivateIP          string
	SSHUser            string
	SSHPort            int
	PrivateKey         string
	PrivateKeyPass     string
	PricePerHour       float64
	VCPUs              int
	RAMGiB             int
	StorageGiB         int
	ArchitectureType   string
}

type ElestioActionResponse struct {
	Status  string      `json:"status"`
	Message string      `json:"message"`
	Error   string      `json:"error"`
	Result  interface{} `json:"result"`
	Data    interface{} `json:"data"`
}

type ElestioServiceSummary struct {
	ID               string `json:"id"`
	VMID             string `json:"vmID"`
	DisplayName      string `json:"displayName"`
	ServerName       string `json:"serverName"`
	ProjectID        string `json:"projectID"`
	Status           string `json:"status"`
	DeploymentStatus string `json:"deploymentStatus"`
	IPv4             string `json:"ipv4"`
	GlobalIP         string `json:"globalIP"`
	CNAME            string `json:"cname"`
}

type ElestioProjectSummary struct {
	ProjectID   string `json:"projectID"`
	ProjectName string `json:"project_name"`
}

func NewElestioClient(cfg ElestioConfig) *ElestioClient {
	return &ElestioClient{
		http: NewHTTPClient(cfg.BaseURL, 60*time.Second, nil),
	}
}

func (c *ElestioClient) GetJWT(email, token string) (string, error) {
	body := map[string]string{
		"email": email,
		"token": token,
	}
	var resp struct {
		Status string `json:"status"`
		JWT    string `json:"jwt"`
	}
	if err := c.http.DoJSON("POST", "/api/auth/checkAPIToken", nil, body, &resp); err != nil {
		return "", err
	}
	if resp.JWT == "" {
		return "", fmt.Errorf("elestio did not return jwt")
	}
	return resp.JWT, nil
}

func (c *ElestioClient) TestBYOVMConnection(req TestBYOVMRequest) error {
	body := map[string]string{
		"jwt":               strings.TrimSpace(req.JWT),
		"userName":          nonEmptyString(req.UserName, "root"),
		"port":              strconv.Itoa(nonZero(req.Port, 22)),
		"ipv4":              strings.TrimSpace(req.IPv4),
		"privateKeyContent": req.PrivateKeyContent,
		"passphrase":        req.Passphrase,
	}

	if body["jwt"] == "" {
		return fmt.Errorf("missing jwt for BYOVM connection test")
	}
	if body["ipv4"] == "" {
		return fmt.Errorf("missing ipv4 for BYOVM connection test")
	}

	var resp ElestioActionResponse
	if err := c.http.DoJSON("POST", "/api/servers/getBYOVM", nil, body, &resp); err != nil {
		return err
	}
	if !isElestioStatusOK(resp.Status) {
		return fmt.Errorf("elestio getBYOVM failed: %s", nonEmptyString(resp.Message, resp.Error))
	}
	return nil
}

func (c *ElestioClient) CreateBYOVMService(req CreateBYOVMServiceRequest) (ElestioActionResponse, error) {
	if strings.TrimSpace(req.JWT) == "" {
		return ElestioActionResponse{}, fmt.Errorf("missing jwt for createServer")
	}
	if req.TemplateID <= 0 {
		return ElestioActionResponse{}, fmt.Errorf("invalid template id for createServer")
	}
	if strings.TrimSpace(req.ProjectID) == "" {
		return ElestioActionResponse{}, fmt.Errorf("missing project id for createServer")
	}
	if strings.TrimSpace(req.AdminEmail) == "" {
		return ElestioActionResponse{}, fmt.Errorf("missing admin email for createServer")
	}
	if strings.TrimSpace(req.ServerName) == "" {
		return ElestioActionResponse{}, fmt.Errorf("missing server name for createServer")
	}
	if strings.TrimSpace(req.PublicIPv4) == "" {
		return ElestioActionResponse{}, fmt.Errorf("missing byovm public ipv4 for createServer")
	}

	serverType := fmt.Sprintf("CUSTOM-%dC-%dG", nonZero(req.VCPUs, 2), nonZero(req.RAMGiB, 4))
	appID := nonEmptyString(req.AppID, "CloudVM")
	support := nonEmptyString(req.SupportPlan, "level1")
	serviceType := nonEmptyString(req.ServiceType, "Service")
	deploymentType := nonEmptyString(req.DeploymentType, "normal")
	providerName := nonEmptyString(req.ProviderName, "BYOVM")
	providerCustomName := nonEmptyString(req.ProviderCustomName, "whitesky.cloud")
	datacenter := strings.TrimSpace(req.Datacenter)
	if datacenter == "" {
		datacenter = "custom"
	}
	version := strings.TrimSpace(req.TemplateVersion)
	if version == "" {
		version = "latest"
	}
	architecture := nonEmptyString(req.ArchitectureType, "amd64")
	sshUser := nonEmptyString(req.SSHUser, "root")
	sshPort := nonZero(req.SSHPort, 22)
	storage := nonZero(req.StorageGiB, 40)

	body := map[string]interface{}{
		"serviceType":           serviceType,
		"deploymentServiceType": deploymentType,
		"appid":                 appID,
		"jwt":                   req.JWT,
		"templateID":            req.TemplateID,
		"serverType":            serverType,
		"datacenter":            datacenter,
		"serverName":            req.ServerName,
		"adminEmail":            req.AdminEmail,
		"providerName":          providerName,
		"domain":                "",
		"senderDomain":          "",
		"support":               support,
		"data":                  "",
		"projectId":             req.ProjectID,
		"version":               version,
		"autoUpdateData": map[string]interface{}{
			"app_AutoUpdate_Enabled":                true,
			"app_AutoUpdate_DayOfWeek":              "0",
			"app_AutoUpdate_Hour":                   "1",
			"app_AutoUpdate_Minute":                 "00",
			"system_AutoUpdate_Enabled":             true,
			"system_AutoUpdate_SecurityPatchesOnly": true,
			"system_AutoUpdate_RebootDayOfWeek":     "0",
			"system_AutoUpdate_RebootHour":          "5",
			"system_AutoUpdate_RebootMinute":        "00",
		},
		"selectedGlobalKeys": []string{},
		"isReplica":          false,
		"primaryServerID":    nil,
		"createdFrom":        "app",
		"byovm":              true,
		"byovmPayload": map[string]interface{}{
			"ipv4":                 req.PublicIPv4,
			"ipv6":                 req.IPv6,
			"privateIP":            req.PrivateIP,
			"providerCustomName":   providerCustomName,
			"datacenter":           datacenter,
			"title":                serverType,
			"userName":             sshUser,
			"port":                 strconv.Itoa(sshPort),
			"privateKey":           req.PrivateKey,
			"privateKeypassphrase": req.PrivateKeyPass,
			"pricePerHour":         req.PricePerHour,
			"byovm":                true,
			"architectureType":     architecture,
			"gpu":                  0,
			"cpuType":              "normal",
			"gpuName":              "",
			"minCpu":               "",
			"minRam":               "",
			"minStorage":           "",
			"cpuPrice":             "",
			"gpuPrice":             "",
			"ramPrice":             "",
			"storagePrice":         "",
		},
		"planDetails": []map[string]interface{}{
			{
				"title":                  serverType,
				"vCPU":                   strconv.Itoa(nonZero(req.VCPUs, 2)),
				"ramGB":                  strconv.Itoa(nonZero(req.RAMGiB, 4)),
				"storageSizeGB":          strconv.Itoa(storage),
				"offerTrafficIncludedGB": "0",
				"pricePerHour":           req.PricePerHour,
				"architectureType":       architecture,
				"gpu":                    0,
				"cpuType":                "normal",
				"gpuName":                "",
				"minCpu":                 "",
				"minRam":                 "",
				"minStorage":             "",
				"cpuPrice":               "",
				"gpuPrice":               "",
				"ramPrice":               "",
				"storagePrice":           "",
				"cpu":                    strconv.Itoa(nonZero(req.VCPUs, 2)),
				"ram":                    strconv.Itoa(nonZero(req.RAMGiB, 4)),
				"hdd":                    strconv.Itoa(storage),
				"bandwidth":              "0",
			},
		},
	}

	var resp ElestioActionResponse
	if err := c.http.DoJSON("POST", "/api/servers/createServer", nil, body, &resp); err != nil {
		return ElestioActionResponse{}, err
	}
	if !isElestioStatusOK(resp.Status) {
		return ElestioActionResponse{}, fmt.Errorf("elestio createServer failed: %s", nonEmptyString(resp.Message, resp.Error))
	}
	return resp, nil
}

func (c *ElestioClient) DeleteServer(jwt, projectID, serverID string, deleteServiceWithBackup bool) error {
	if strings.TrimSpace(jwt) == "" {
		return fmt.Errorf("missing jwt for deleteServer")
	}
	if strings.TrimSpace(projectID) == "" {
		return fmt.Errorf("missing project id for deleteServer")
	}
	if strings.TrimSpace(serverID) == "" {
		return fmt.Errorf("missing server id for deleteServer")
	}

	body := map[string]string{
		"projectID":                 strings.TrimSpace(projectID),
		"vmID":                      strings.TrimSpace(serverID),
		"jwt":                       strings.TrimSpace(jwt),
		"isDeleteServiceWithBackup": strconv.FormatBool(deleteServiceWithBackup),
	}

	var resp ElestioActionResponse
	if err := c.http.DoJSON("POST", "/api/servers/deleteServer", nil, body, &resp); err != nil {
		return err
	}
	if !isElestioStatusOK(resp.Status) {
		return fmt.Errorf("elestio deleteServer failed: %s", nonEmptyString(resp.Message, resp.Error))
	}
	return nil
}

func (c *ElestioClient) ListServices(jwt, projectID string) ([]ElestioServiceSummary, error) {
	if strings.TrimSpace(jwt) == "" {
		return nil, fmt.Errorf("missing jwt for getServices")
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("missing project id for getServices")
	}

	body := map[string]string{
		"jwt":             strings.TrimSpace(jwt),
		"projectId":       strings.TrimSpace(projectID),
		"isActiveService": "true",
	}

	var resp []ElestioServiceSummary
	if err := c.http.DoJSON("POST", "/api/servers/getServices", nil, body, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *ElestioClient) ListProjects(jwt string) ([]ElestioProjectSummary, error) {
	if strings.TrimSpace(jwt) == "" {
		return nil, fmt.Errorf("missing jwt for getList")
	}

	body := map[string]string{
		"jwt": strings.TrimSpace(jwt),
	}
	var resp struct {
		Status string `json:"status"`
		Data   struct {
			Projects []ElestioProjectSummary `json:"projects"`
		} `json:"data"`
	}
	if err := c.http.DoJSON("POST", "/api/projects/getList", nil, body, &resp); err != nil {
		return nil, err
	}
	if !isElestioStatusOK(resp.Status) && strings.TrimSpace(resp.Status) != "" {
		return nil, fmt.Errorf("elestio getList failed: %s", resp.Status)
	}
	return resp.Data.Projects, nil
}

func (c *ElestioClient) AddSSLDomain(jwt, vmID, domain string) error {
	if strings.TrimSpace(jwt) == "" {
		return fmt.Errorf("missing jwt for SSLDomainsAdd")
	}
	if strings.TrimSpace(vmID) == "" {
		return fmt.Errorf("missing vm id for SSLDomainsAdd")
	}
	if strings.TrimSpace(domain) == "" {
		return fmt.Errorf("missing domain for SSLDomainsAdd")
	}

	body := map[string]string{
		"vmID":   strings.TrimSpace(vmID),
		"jwt":    strings.TrimSpace(jwt),
		"action": "SSLDomainsAdd",
		"domain": strings.TrimSpace(domain),
	}

	var resp ElestioActionResponse
	if err := c.http.DoJSON("POST", "/api/servers/DoActionOnServer", nil, body, &resp); err != nil {
		return err
	}
	if !isElestioStatusOK(resp.Status) {
		return fmt.Errorf("elestio SSLDomainsAdd failed: %s", nonEmptyString(resp.Message, resp.Error))
	}
	return nil
}

func (c *ElestioClient) ListSSLDomains(jwt, vmID string) ([]string, error) {
	if strings.TrimSpace(jwt) == "" {
		return nil, fmt.Errorf("missing jwt for SSLDomainsList")
	}
	if strings.TrimSpace(vmID) == "" {
		return nil, fmt.Errorf("missing vm id for SSLDomainsList")
	}

	body := map[string]string{
		"vmID":   strings.TrimSpace(vmID),
		"jwt":    strings.TrimSpace(jwt),
		"action": "SSLDomainsList",
	}

	var payload interface{}
	if err := c.http.DoJSON("POST", "/api/servers/DoActionOnServer", nil, body, &payload); err != nil {
		return nil, err
	}
	if !isElestioPayloadStatusOK(payload) {
		return nil, fmt.Errorf("elestio SSLDomainsList failed")
	}
	return extractDomainsFromAny(payload), nil
}

func (c *ElestioClient) GetServerDetails(jwt, projectID, vmID string) (ElestioServiceSummary, error) {
	if strings.TrimSpace(jwt) == "" {
		return ElestioServiceSummary{}, fmt.Errorf("missing jwt for getServerDetails")
	}
	if strings.TrimSpace(projectID) == "" {
		return ElestioServiceSummary{}, fmt.Errorf("missing project id for getServerDetails")
	}
	if strings.TrimSpace(vmID) == "" {
		return ElestioServiceSummary{}, fmt.Errorf("missing vm id for getServerDetails")
	}

	body := map[string]string{
		"jwt":       strings.TrimSpace(jwt),
		"vmID":      strings.TrimSpace(vmID),
		"projectID": strings.TrimSpace(projectID),
	}

	var payload map[string]interface{}
	if err := c.http.DoJSON("POST", "/api/servers/getServerDetails", nil, body, &payload); err != nil {
		return ElestioServiceSummary{}, err
	}
	return parseElestioServiceDetailsPayload(payload)
}

func parseElestioServiceDetailsPayload(payload map[string]interface{}) (ElestioServiceSummary, error) {
	items, ok := payload["serviceInfos"].([]interface{})
	if !ok || len(items) == 0 {
		return ElestioServiceSummary{}, fmt.Errorf("elestio getServerDetails did not return serviceInfos")
	}
	raw, ok := items[0].(map[string]interface{})
	if !ok {
		return ElestioServiceSummary{}, fmt.Errorf("elestio getServerDetails returned invalid serviceInfos payload")
	}

	return ElestioServiceSummary{
		ID:               stringFromAny(raw["id"]),
		VMID:             firstNonEmptyString(raw["providerServerID"], raw["vmID"], raw["serverID"], raw["serverId"]),
		DisplayName:      stringFromAny(raw["displayName"]),
		ServerName:       stringFromAny(raw["serverName"]),
		ProjectID:        firstNonEmptyString(raw["projectID"], raw["projectId"]),
		Status:           stringFromAny(raw["status"]),
		DeploymentStatus: stringFromAny(raw["deploymentStatus"]),
		IPv4:             stringFromAny(raw["ipv4"]),
		GlobalIP:         stringFromAny(raw["globalIP"]),
		CNAME:            stringFromAny(raw["cname"]),
	}, nil
}

func (c *ElestioClient) GetServerReachableDomains(jwt, projectID, vmID string) ([]string, error) {
	if strings.TrimSpace(jwt) == "" {
		return nil, fmt.Errorf("missing jwt for getServerDetails")
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("missing project id for getServerDetails")
	}
	if strings.TrimSpace(vmID) == "" {
		return nil, fmt.Errorf("missing vm id for getServerDetails")
	}

	body := map[string]string{
		"jwt":       strings.TrimSpace(jwt),
		"vmID":      strings.TrimSpace(vmID),
		"projectID": strings.TrimSpace(projectID),
	}

	var payload interface{}
	if err := c.http.DoJSON("POST", "/api/servers/getServerDetails", nil, body, &payload); err != nil {
		return nil, err
	}
	return extractDomainsFromAny(payload), nil
}

func isElestioPayloadStatusOK(payload interface{}) bool {
	m, ok := payload.(map[string]interface{})
	if !ok {
		return true
	}
	s, ok := m["status"]
	if !ok {
		return true
	}
	return isElestioStatusOK(stringFromAny(s))
}

func extractDomainsFromAny(v interface{}) []string {
	found := make(map[string]bool)
	collectDomains(v, found)
	out := make([]string, 0, len(found))
	for d := range found {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func collectDomains(v interface{}, found map[string]bool) {
	switch t := v.(type) {
	case string:
		if d := normalizeDomainCandidate(t); d != "" {
			found[d] = true
		}
	case []interface{}:
		for _, item := range t {
			collectDomains(item, found)
		}
	case map[string]interface{}:
		for key, value := range t {
			lk := strings.ToLower(strings.TrimSpace(key))
			if lk == "status" || lk == "message" || lk == "error" || lk == "token" {
				continue
			}
			collectDomains(value, found)
		}
	}
}

func normalizeDomainCandidate(value string) string {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return ""
	}

	candidate := raw
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		candidate = u.Host
	}

	candidate = strings.ToLower(strings.TrimSpace(candidate))
	candidate = strings.Trim(candidate, ".")
	if i := strings.Index(candidate, "/"); i >= 0 {
		candidate = candidate[:i]
	}
	if i := strings.Index(candidate, ":"); i >= 0 {
		candidate = candidate[:i]
	}

	if strings.Count(candidate, ".") < 1 {
		return ""
	}
	for _, r := range candidate {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !isAlphaNum && r != '.' && r != '-' {
			return ""
		}
	}
	return candidate
}

func InferElestioProjectID(jwt string) string {
	parts := strings.Split(jwt, ".")
	if len(parts) < 2 {
		return ""
	}

	raw, err := decodeJWTPart(parts[1])
	if err != nil {
		return ""
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return ""
	}

	user, ok := claims["user"].(map[string]interface{})
	if !ok {
		return ""
	}

	for _, key := range []string{"projectID", "projectId", "project_id"} {
		if id := stringFromAny(user[key]); strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

func (c *ElestioClient) GetTemplateSpec(templateID int) (TemplateSpec, error) {
	templates, err := c.ListTemplates()
	if err != nil {
		return TemplateSpec{}, err
	}
	if len(templates) == 0 {
		return TemplateSpec{}, fmt.Errorf("no templates returned by elestio")
	}

	for _, tpl := range templates {
		if tpl.TemplateID == templateID {
			return tpl, nil
		}
	}

	return TemplateSpec{}, fmt.Errorf("template id %d not found in elestio template catalog", templateID)
}

func (c *ElestioClient) ListTemplates() ([]TemplateSpec, error) {
	var payload interface{}
	if err := c.http.DoJSON("GET", "/api/servers/getTemplates", nil, nil, &payload); err != nil {
		return nil, err
	}
	templates := extractTemplateList(payload)
	if len(templates) == 0 {
		return nil, fmt.Errorf("no templates returned by elestio")
	}

	out := make([]TemplateSpec, 0, len(templates))
	for _, raw := range templates {
		out = append(out, parseTemplateSpec(raw))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].TemplateID < out[j].TemplateID
	})

	return out, nil
}

func parseTemplateSpec(raw map[string]interface{}) TemplateSpec {
	spec := TemplateSpec{
		TemplateID:      intFromAny(raw["id"]),
		TemplateName:    stringFromAny(raw["title"]),
		Category:        stringFromAny(raw["category"]),
		Description:     stringFromAny(raw["description"]),
		TemplateVersion: stringFromAny(raw["version"]),
		RawTemplate:     raw,
	}
	spec.BootstrapCommand = findBootstrapCommand(raw)
	spec.Ports = extractPorts(raw)
	return spec
}

func extractTemplateList(payload interface{}) []map[string]interface{} {
	obj, ok := payload.(map[string]interface{})
	if ok {
		if arr, ok := obj["instances"].([]interface{}); ok {
			return interfaceArrayToMapArray(arr)
		}
		if arr, ok := obj["result"].([]interface{}); ok {
			return interfaceArrayToMapArray(arr)
		}
	}
	if arr, ok := payload.([]interface{}); ok {
		return interfaceArrayToMapArray(arr)
	}
	return nil
}

func interfaceArrayToMapArray(arr []interface{}) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

func findBootstrapCommand(raw map[string]interface{}) string {
	keys := []string{
		"bootstrapCommand",
		"bootstrap_command",
		"deployCommand",
		"deploy_command",
		"command",
		"installCommand",
		"install_command",
		"setupCommand",
		"setup_command",
		"sshCommand",
		"ssh_command",
	}
	for _, key := range keys {
		if v, ok := raw[key]; ok {
			cmd := strings.TrimSpace(stringFromAny(v))
			if cmd != "" {
				return cmd
			}
		}
	}
	if v, ok := raw["data"].(map[string]interface{}); ok {
		return findBootstrapCommand(v)
	}
	return ""
}

func extractPorts(raw map[string]interface{}) []int {
	found := map[int]bool{}

	// Numeric/object arrays (ports, exposedPorts, firewallRules, etc).
	for _, key := range []string{"ports", "exposedPorts", "firewallRules", "firewall_ports"} {
		if arr, ok := raw[key].([]interface{}); ok {
			for _, item := range arr {
				switch t := item.(type) {
				case float64:
					if int(t) > 0 {
						found[int(t)] = true
					}
				case string:
					for _, p := range parseCSVPorts(t) {
						found[p] = true
					}
				case map[string]interface{}:
					for _, k := range []string{"port", "localPort", "listeningPort", "targetPort", "publicPort", "hostPort"} {
						p := intFromAny(t[k])
						if p > 0 {
							found[p] = true
						}
					}
				}
			}
		}
	}

	// Comma-separated firewall ports.
	for _, key := range []string{"firewallPorts", "firewall_ports", "managedDBPort"} {
		if v := stringFromAny(raw[key]); v != "" {
			for _, p := range parseCSVPorts(v) {
				found[p] = true
			}
		}
	}

	if nested, ok := raw["data"].(map[string]interface{}); ok {
		for _, p := range extractPorts(nested) {
			found[p] = true
		}
	}

	ports := make([]int, 0, len(found))
	for p := range found {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	return ports
}

func parseCSVPorts(value string) []int {
	parts := regexp.MustCompile(`[,;\s]+`).Split(strings.TrimSpace(value), -1)
	ports := make([]int, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err == nil && n > 0 {
			ports = append(ports, n)
		}
	}
	return ports
}

func intFromAny(v interface{}) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err == nil {
			return n
		}
	}
	return 0
}

func stringFromAny(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.Itoa(int(t))
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		return ""
	}
}

func firstNonEmptyString(values ...interface{}) string {
	for _, value := range values {
		if s := strings.TrimSpace(stringFromAny(value)); s != "" {
			return s
		}
	}
	return ""
}

func decodeJWTPart(value string) ([]byte, error) {
	if m := len(value) % 4; m != 0 {
		value += strings.Repeat("=", 4-m)
	}
	return base64.URLEncoding.DecodeString(value)
}

func isElestioStatusOK(status string) bool {
	s := strings.ToUpper(strings.TrimSpace(status))
	return s == "" || s == "OK" || s == "SUCCESS"
}

func nonZero(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func nonEmptyString(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}
