package clients

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type WhiteSkyClient struct {
	http       *HTTPClient
	customerID string
	cloudspace string
	iamBaseURL string
	token      string
	mu         sync.Mutex
}

type WhiteSkyConfig struct {
	BaseURL        string
	IAMBaseURL     string
	Token          string
	CustomerID     string
	CloudspaceID   string
	RequestTimeout time.Duration
}

type CreateVMRequest struct {
	Name        string
	Description string
	VCPUs       int
	MemoryMiB   int
	ImageID     int
	DiskSizeGiB int
}

type Note struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

type PortForward struct {
	PortforwardID string `json:"portforward_id"`
	Protocol      string `json:"protocol"`
	LocalPort     int    `json:"local_port"`
	PublicPort    int    `json:"public_port"`
	PublicIP      string `json:"public_ip"`
	VMID          int    `json:"vm_id"`
}

type VMNetworkInterface struct {
	IPAddress string `json:"ip_address"`
	NICType   string `json:"nic_type"`
}

type VMInfo struct {
	VMID              int                  `json:"vm_id"`
	Name              string               `json:"name"`
	Status            string               `json:"status"`
	VCPUs             int                  `json:"vcpus"`
	MemoryMiB         int                  `json:"memory"`
	NetworkInterfaces []VMNetworkInterface `json:"network_interfaces"`
}

type VMDisk struct {
	DiskID    int    `json:"disk_id"`
	DiskSize  int    `json:"disk_size"`
	DiskName  string `json:"disk_name"`
	DiskType  string `json:"disk_type"`
	Status    string `json:"status"`
	Order     string `json:"order"`
	PciBus    int    `json:"pci_bus"`
	PciSlot   int    `json:"pci_slot"`
	Exposed   bool   `json:"exposed"`
	ExtraDesc string `json:"description"`
}

type CloudspaceInfo struct {
	CloudspaceID      string `json:"cloudspace_id"`
	Name              string `json:"name"`
	Location          string `json:"location"`
	ExternalNetworkIP string `json:"external_network_ip"`
}

type CloudspaceExternalNetwork struct {
	ExternalNetworkID string `json:"external_network_id"`
	ExternalNetworkIP string `json:"external_network_ip"`
	Type              string `json:"type"`
}

type VMImage struct {
	ImageID int    `json:"image_id"`
	Name    string `json:"name"`
	OSName  string `json:"os_name"`
	OSType  string `json:"os_type"`
}

type LoadBalancerSimple struct {
	LoadBalancerID string `json:"loadbalancer_id"`
	Name           string `json:"name"`
	Type           string `json:"type"`
}

type LoadBalancerFrontEnd struct {
	Port      int    `json:"port"`
	IPAddress string `json:"ip_address"`
}

type LoadBalancerInfo struct {
	LoadBalancerID string               `json:"loadbalancer_id"`
	Name           string               `json:"name"`
	Type           string               `json:"type"`
	FrontEnd       LoadBalancerFrontEnd `json:"front_end"`
}

type ReverseProxySimple struct {
	ReverseProxyID string `json:"reverseproxy_id"`
	Name           string `json:"name"`
}

type ReverseProxyFrontEnd struct {
	HTTPPort  int    `json:"http_port"`
	HTTPSPort int    `json:"https_port"`
	IPAddress string `json:"ip_address"`
}

type ReverseProxyInfo struct {
	ReverseProxyID string               `json:"reverseproxy_id"`
	Name           string               `json:"name"`
	FrontEnd       ReverseProxyFrontEnd `json:"front_end"`
}

type ExternalNetwork struct {
	ExternalNetworkID int    `json:"external_network_id"`
	Name              string `json:"name"`
	Public            bool   `json:"public"`
}

type TopLevelDomain struct {
	Domain string `json:"domain"`
	Valid  bool   `json:"valid"`
}

func NewWhiteSkyClient(cfg WhiteSkyConfig) *WhiteSkyClient {
	timeout := cfg.RequestTimeout
	if timeout < 300*time.Second {
		timeout = 300 * time.Second
	}

	client := &WhiteSkyClient{
		customerID: cfg.CustomerID,
		cloudspace: cfg.CloudspaceID,
		iamBaseURL: strings.TrimRight(strings.TrimSpace(cfg.IAMBaseURL), "/"),
		token:      strings.TrimSpace(cfg.Token),
	}
	client.http = NewHTTPClientWithEditor(cfg.BaseURL, timeout, nil, client.authorizeRequest)
	return client
}

func (c *WhiteSkyClient) authorizeRequest(req *http.Request) error {
	token, err := c.currentToken()
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func (c *WhiteSkyClient) currentToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if tokenNeedsRefresh(c.token) {
		refreshed, err := refreshWhiteSkyJWT(c.iamBaseURL, c.token, c.http.httpClient.Timeout)
		if err != nil {
			return "", err
		}
		c.token = refreshed
	}
	if strings.TrimSpace(c.token) == "" {
		return "", fmt.Errorf("missing whitesky.cloud jwt token")
	}
	return c.token, nil
}

func tokenNeedsRefresh(token string) bool {
	expiry, ok := jwtExpiry(token)
	if !ok {
		return false
	}
	return !time.Now().Add(time.Minute).Before(expiry)
}

func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(parts[1])
	}
	if err != nil {
		return time.Time{}, false
	}
	var payload struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(payload.Exp, 0), true
}

func refreshWhiteSkyJWT(iamBaseURL, token string, timeout time.Duration) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(iamBaseURL), "/")
	if baseURL == "" {
		return "", fmt.Errorf("whitesky.cloud JWT expired and no IAM base URL is configured; set PAASCTL_WHITESKY_IAM_BASE_URL or whitesky.iam_base_url")
	}
	if strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("missing whitesky.cloud jwt token")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	req, err := http.NewRequest("GET", baseURL+"/v1/oauth/jwt/refresh", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	if debugHTTP {
		logRequest(req, nil)
	}

	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to refresh whitesky.cloud jwt: %w", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if debugHTTP {
		logResponse(resp, body)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("failed to refresh whitesky.cloud jwt: GET %s failed with status %d: %s", req.URL.String(), resp.StatusCode, string(body))
	}
	return parseRefreshedWhiteSkyJWT(body)
}

func looksLikeJWT(value string) bool {
	parts := strings.Split(strings.TrimSpace(value), ".")
	return len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != ""
}

func parseRefreshedWhiteSkyJWT(body []byte) (string, error) {
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err == nil {
		for _, key := range []string{"jwt", "token", "access_token"} {
			if raw, ok := payload[key]; ok {
				if refreshed := strings.TrimSpace(fmt.Sprintf("%v", raw)); looksLikeJWT(refreshed) {
					return refreshed, nil
				}
			}
		}
	}

	if refreshed := strings.Trim(strings.TrimSpace(string(body)), "\""); looksLikeJWT(refreshed) {
		return refreshed, nil
	}
	return "", fmt.Errorf("failed to refresh whitesky.cloud jwt: refresh response did not contain a jwt")
}

func (c *WhiteSkyClient) CreateVM(req CreateVMRequest) (int, error) {
	query := map[string]string{
		"name":        req.Name,
		"description": req.Description,
		"vcpus":       strconv.Itoa(req.VCPUs),
		"memory":      strconv.Itoa(req.MemoryMiB),
		"start_vm":    "true",
	}
	if req.ImageID > 0 {
		query["image_id"] = strconv.Itoa(req.ImageID)
	}
	if req.DiskSizeGiB > 0 {
		query["disk_size"] = strconv.Itoa(req.DiskSizeGiB)
	}

	var resp struct {
		VMID int `json:"vm_id"`
	}
	if err := c.http.DoJSON("POST", c.path("/vms"), query, nil, &resp); err != nil {
		return 0, err
	}
	if resp.VMID == 0 {
		return 0, fmt.Errorf("create VM returned empty vm_id")
	}
	return resp.VMID, nil
}

func (c *WhiteSkyClient) GetCloudspaceInfo() (CloudspaceInfo, error) {
	var resp CloudspaceInfo
	err := c.http.DoJSON("GET", fmt.Sprintf("/customers/%s/cloudspaces/%s", c.customerID, c.cloudspace), nil, nil, &resp)
	return resp, err
}

func (c *WhiteSkyClient) ListLocationImages(location string) ([]VMImage, error) {
	var resp struct {
		Result []VMImage `json:"result"`
	}
	path := fmt.Sprintf("/customers/%s/locations/%s/vm-images", c.customerID, location)
	if err := c.http.DoJSON("GET", path, nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func (c *WhiteSkyClient) GetVM(vmID int) (VMInfo, error) {
	var resp VMInfo
	err := c.http.DoJSON("GET", c.path("/vms/%d", vmID), nil, nil, &resp)
	return resp, err
}

func (c *WhiteSkyClient) DeleteVM(vmID int, permanently bool) error {
	query := map[string]string{
		"permanently": strconv.FormatBool(permanently),
	}
	return c.http.DoJSON("DELETE", c.path("/vms/%d", vmID), query, nil, nil)
}

func (c *WhiteSkyClient) ResizeVMMemory(vmID int, memoryMiB int) error {
	if vmID <= 0 {
		return fmt.Errorf("invalid vm id")
	}
	if memoryMiB <= 0 {
		return fmt.Errorf("memory must be positive")
	}
	query := map[string]string{
		"memory": strconv.Itoa(memoryMiB),
	}
	return c.http.DoJSON("PUT", c.path("/vms/%d/size", vmID), query, nil, nil)
}

func (c *WhiteSkyClient) ResizeVMVCPUs(vmID int, vcpus int) error {
	if vmID <= 0 {
		return fmt.Errorf("invalid vm id")
	}
	if vcpus <= 0 {
		return fmt.Errorf("vcpus must be positive")
	}
	query := map[string]string{
		"vcpus": strconv.Itoa(vcpus),
	}
	return c.http.DoJSON("PUT", c.path("/vms/%d/size", vmID), query, nil, nil)
}

func (c *WhiteSkyClient) ListVMDisks(vmID int) ([]VMDisk, error) {
	var resp struct {
		Result []VMDisk `json:"result"`
	}
	if err := c.http.DoJSON("GET", c.path("/vms/%d/disks", vmID), nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func (c *WhiteSkyClient) ResizeDisk(location string, diskID int, diskSizeGiB int) error {
	loc := strings.TrimSpace(location)
	if loc == "" {
		return fmt.Errorf("location is required to resize disk")
	}
	if diskID <= 0 {
		return fmt.Errorf("invalid disk id")
	}
	if diskSizeGiB <= 0 {
		return fmt.Errorf("disk size must be positive")
	}
	path := fmt.Sprintf("/customers/%s/locations/%s/disks/%d/size", c.customerID, loc, diskID)
	query := map[string]string{
		"disk_size": strconv.Itoa(diskSizeGiB),
	}
	return c.http.DoJSON("PUT", path, query, nil, nil)
}

func (c *WhiteSkyClient) ListPortForwards() ([]PortForward, error) {
	var resp struct {
		Result []PortForward `json:"result"`
	}
	if err := c.http.DoJSON("GET", c.path("/portforwards"), nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func (c *WhiteSkyClient) CreatePortForward(localPort, publicPort, vmID int, publicIP, protocol string) (string, error) {
	if localPort <= 0 {
		return "", fmt.Errorf("invalid local port")
	}
	if publicPort <= 0 {
		return "", fmt.Errorf("invalid public port")
	}
	if vmID <= 0 {
		return "", fmt.Errorf("invalid vm id")
	}
	proto := strings.ToLower(strings.TrimSpace(protocol))
	if proto == "" {
		proto = "tcp"
	}
	if proto != "tcp" && proto != "udp" {
		return "", fmt.Errorf("invalid protocol %q", protocol)
	}

	query := map[string]string{
		"local_port":  strconv.Itoa(localPort),
		"public_port": strconv.Itoa(publicPort),
		"vm_id":       strconv.Itoa(vmID),
		"protocol":    proto,
	}
	if strings.TrimSpace(publicIP) != "" {
		query["public_ip"] = strings.TrimSpace(publicIP)
	}

	var resp struct {
		ID            string `json:"id"`
		PortforwardID string `json:"portforward_id"`
	}
	if err := c.http.DoJSON("POST", c.path("/portforwards"), query, nil, &resp); err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.PortforwardID) != "" {
		return resp.PortforwardID, nil
	}
	if strings.TrimSpace(resp.ID) != "" {
		return resp.ID, nil
	}
	if resp.ID == "" {
		return "", fmt.Errorf("create portforward returned empty id")
	}
	return resp.ID, nil
}

func (c *WhiteSkyClient) DeletePortForward(portforwardID string) error {
	return c.http.DoJSON("DELETE", c.path("/portforwards/%s", portforwardID), nil, nil, nil)
}

func (c *WhiteSkyClient) ListCloudspaceExternalNetworks() ([]CloudspaceExternalNetwork, error) {
	var resp struct {
		Result []CloudspaceExternalNetwork `json:"result"`
	}
	if err := c.http.DoJSON("GET", c.path("/external-networks"), nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func (c *WhiteSkyClient) AddCloudspaceExternalNetwork(externalNetworkID, externalNetworkType, externalNetworkIP string) (CloudspaceExternalNetwork, error) {
	query := map[string]string{
		"external_network_id":   externalNetworkID,
		"external_network_type": externalNetworkType,
		"external_network_ip":   externalNetworkIP,
	}
	var resp struct {
		ExternalNetworkID int    `json:"external_network_id"`
		ExternalNetworkIP string `json:"external_network_ip"`
	}
	if err := c.http.DoJSON("POST", c.path("/external-networks"), query, nil, &resp); err != nil {
		return CloudspaceExternalNetwork{}, err
	}
	id := externalNetworkID
	if resp.ExternalNetworkID > 0 {
		id = strconv.Itoa(resp.ExternalNetworkID)
	}
	ip := externalNetworkIP
	if resp.ExternalNetworkIP != "" {
		ip = resp.ExternalNetworkIP
	}
	return CloudspaceExternalNetwork{
		ExternalNetworkID: id,
		ExternalNetworkIP: ip,
		Type:              externalNetworkType,
	}, nil
}

func (c *WhiteSkyClient) RemoveCloudspaceExternalNetwork(externalNetworkID, externalNetworkIP string) error {
	query := map[string]string{
		"external_network_id": externalNetworkID,
		"external_network_ip": externalNetworkIP,
	}
	return c.http.DoJSON("DELETE", c.path("/external-networks"), query, nil, nil)
}

func (c *WhiteSkyClient) AddVMExternalNICDomain(vmID int, externalIPAddress, domain string) error {
	if vmID <= 0 {
		return fmt.Errorf("invalid vm id for add nic domain")
	}
	ip := strings.TrimSpace(externalIPAddress)
	if ip == "" {
		return fmt.Errorf("missing external ip for add nic domain")
	}
	d := strings.TrimSpace(domain)
	if d == "" {
		return fmt.Errorf("missing domain for add nic domain")
	}

	path := fmt.Sprintf(
		"/alpha/customers/%s/cloudspaces/%s/vms/%d/external-nics/%s/dns",
		c.customerID,
		c.cloudspace,
		vmID,
		url.PathEscape(ip),
	)
	query := map[string]string{
		"domain": d,
		"type":   "A",
	}
	return c.http.DoJSON("POST", path, query, nil, nil)
}

func (c *WhiteSkyClient) DeleteVMExternalNICDomain(vmID int, externalIPAddress, domain string) error {
	if vmID <= 0 {
		return fmt.Errorf("invalid vm id for delete nic domain")
	}
	ip := strings.TrimSpace(externalIPAddress)
	if ip == "" {
		return fmt.Errorf("missing external ip for delete nic domain")
	}
	d := strings.TrimSpace(domain)
	if d == "" {
		return fmt.Errorf("missing domain for delete nic domain")
	}

	path := fmt.Sprintf(
		"/alpha/customers/%s/cloudspaces/%s/vms/%d/external-nics/%s/dns/%s",
		c.customerID,
		c.cloudspace,
		vmID,
		url.PathEscape(ip),
		url.PathEscape(d),
	)
	query := map[string]string{
		"record_type": "A",
	}
	return c.http.DoJSON("DELETE", path, query, nil, nil)
}

func (c *WhiteSkyClient) ListLoadBalancers() ([]LoadBalancerSimple, error) {
	var resp struct {
		Result []LoadBalancerSimple `json:"result"`
	}
	if err := c.http.DoJSON("GET", c.path("/ingress/load-balancers"), nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func (c *WhiteSkyClient) GetLoadBalancer(id string) (LoadBalancerInfo, error) {
	var resp LoadBalancerInfo
	err := c.http.DoJSON("GET", c.path("/ingress/load-balancers/%s", id), nil, nil, &resp)
	return resp, err
}

func (c *WhiteSkyClient) CreateServerPool(name, description string) (string, error) {
	query := map[string]string{"name": name, "description": description}
	var resp struct {
		ID string `json:"id"`
	}
	if err := c.http.DoJSON("POST", c.path("/ingress/server-pools"), query, nil, &resp); err != nil {
		return "", err
	}
	if resp.ID == "" {
		return "", fmt.Errorf("create server pool returned empty id")
	}
	return resp.ID, nil
}

func (c *WhiteSkyClient) DeleteServerPool(id string) error {
	return c.http.DoJSON("DELETE", c.path("/ingress/server-pools/%s", id), nil, nil, nil)
}

func (c *WhiteSkyClient) AddHostToServerPool(serverPoolID, address string) (string, error) {
	query := map[string]string{"address": address}
	var resp struct {
		HostID string `json:"host_id"`
		Addr   string `json:"address"`
	}
	if err := c.http.DoJSON("POST", c.path("/ingress/server-pools/%s/hosts", serverPoolID), query, nil, &resp); err != nil {
		return "", err
	}
	if resp.HostID == "" {
		return "", fmt.Errorf("add host to server pool returned empty host_id")
	}
	return resp.HostID, nil
}

func (c *WhiteSkyClient) RemoveHostFromServerPool(serverPoolID, hostID string) error {
	return c.http.DoJSON("DELETE", c.path("/ingress/server-pools/%s/hosts/%s", serverPoolID, hostID), nil, nil, nil)
}

func (c *WhiteSkyClient) CreateTCPLoadBalancer(name, description, serverPoolID string, publicPort, targetPort int, ipAddress string) (string, error) {
	body := map[string]interface{}{
		"name":        name,
		"description": description,
		"type":        "TCP",
		"front_end": map[string]interface{}{
			"port":       publicPort,
			"ip_address": ipAddress,
		},
		"back_end": map[string]interface{}{
			"serverpool_id": serverPoolID,
			"target_port":   targetPort,
		},
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := c.http.DoJSON("POST", c.path("/ingress/load-balancers"), nil, body, &resp); err != nil {
		return "", err
	}
	if resp.ID == "" {
		return "", fmt.Errorf("create load balancer returned empty id")
	}
	return resp.ID, nil
}

func (c *WhiteSkyClient) DeleteLoadBalancer(id string) error {
	return c.http.DoJSON("DELETE", c.path("/ingress/load-balancers/%s", id), nil, nil, nil)
}

func (c *WhiteSkyClient) ListReverseProxies() ([]ReverseProxySimple, error) {
	var resp struct {
		Result []ReverseProxySimple `json:"result"`
	}
	if err := c.http.DoJSON("GET", c.path("/ingress/reverse-proxies"), nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func (c *WhiteSkyClient) GetReverseProxy(id string) (ReverseProxyInfo, error) {
	var resp ReverseProxyInfo
	err := c.http.DoJSON("GET", c.path("/ingress/reverse-proxies/%s", id), nil, nil, &resp)
	return resp, err
}

func (c *WhiteSkyClient) ListLocationExternalNetworks(location string) ([]ExternalNetwork, error) {
	var resp struct {
		Result []ExternalNetwork `json:"result"`
	}
	path := fmt.Sprintf("/customers/%s/locations/%s/external-networks", c.customerID, location)
	if err := c.http.DoJSON("GET", path, nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func (c *WhiteSkyClient) ListExternalNetworkAvailableIPs(location string, externalNetworkID int) ([]string, error) {
	path := fmt.Sprintf("/customers/%s/locations/%s/external-networks/%d/ip-addresses", c.customerID, location, externalNetworkID)
	var resp struct {
		IPAddresses []string `json:"ip_addresses"`
	}
	if err := c.http.DoJSON("GET", path, nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.IPAddresses, nil
}

func (c *WhiteSkyClient) ListNotes() ([]Note, error) {
	var resp struct {
		Result []Note `json:"result"`
	}
	if err := c.http.DoJSON("GET", c.path("/notes"), nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func (c *WhiteSkyClient) CreateNote(title, content string) error {
	body := map[string]string{
		"title":   title,
		"content": content,
	}
	return c.http.DoJSON("POST", c.path("/notes"), nil, body, nil)
}

func (c *WhiteSkyClient) DeleteNote(noteID string) error {
	return c.http.DoJSON("DELETE", c.path("/notes/%s", noteID), nil, nil, nil)
}

func (c *WhiteSkyClient) ListCustomerTopLevelDomains() ([]TopLevelDomain, error) {
	path := fmt.Sprintf("/alpha/customers/%s/dns/top-level-domains", c.customerID)
	var resp struct {
		Result []TopLevelDomain `json:"result"`
	}
	if err := c.http.DoJSON("GET", path, nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func (c *WhiteSkyClient) GetCustomerVCOTopLevelDomain() (string, error) {
	path := fmt.Sprintf("/alpha/customers/%s/dns/top-level-domain/vco", c.customerID)
	var resp TopLevelDomain
	if err := c.http.DoJSON("GET", path, nil, nil, &resp); err != nil {
		if strings.Contains(err.Error(), "status 404") {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(resp.Domain), nil
}

func (c *WhiteSkyClient) SelectPreferredTopLevelDomain() (string, error) {
	all, err := c.ListCustomerTopLevelDomains()
	if err != nil {
		return "", err
	}
	if len(all) == 0 {
		return "", fmt.Errorf("no top-level domains configured for customer")
	}

	valid := make([]string, 0, len(all))
	for _, tld := range all {
		d := strings.TrimSpace(tld.Domain)
		if d == "" || !tld.Valid {
			continue
		}
		valid = append(valid, strings.ToLower(d))
	}
	if len(valid) == 0 {
		return "", fmt.Errorf("no valid top-level domains configured for customer")
	}
	sort.Strings(valid)
	valid = dedupeDomainStrings(valid)

	vcoDomain, vcoErr := c.GetCustomerVCOTopLevelDomain()
	if vcoErr != nil {
		return "", vcoErr
	}
	vcoDomain = strings.ToLower(strings.TrimSpace(vcoDomain))

	if len(valid) == 1 {
		return valid[0], nil
	}

	for _, d := range valid {
		if !IsSystemProvidedTopLevelDomain(d, vcoDomain) {
			return d, nil
		}
	}
	return valid[0], nil
}

func (c *WhiteSkyClient) ExecCommand(vmID int, command string) (string, error) {
	body := map[string]interface{}{
		"command": "sh",
		"args":    []string{"-lc", command},
	}
	var resp struct {
		Result string `json:"result"`
	}
	if err := c.http.DoJSON("POST", c.path("/vms/%d/exec", vmID), nil, body, &resp); err != nil {
		return "", err
	}
	return resp.Result, nil
}

func (c *WhiteSkyClient) WriteVMFile(vmID int, filepath string, content []byte, appendMode bool) error {
	path := strings.TrimSpace(filepath)
	if path == "" {
		return fmt.Errorf("filepath is required")
	}
	body := map[string]interface{}{
		"filepath": path,
		"content":  base64.StdEncoding.EncodeToString(content),
		"append":   appendMode,
	}
	return c.http.DoJSON("POST", c.path("/vms/%d/file", vmID), nil, body, nil)
}

func (c *WhiteSkyClient) DeleteVMFile(vmID int, filepath string) error {
	path := strings.TrimSpace(filepath)
	if path == "" {
		return fmt.Errorf("filepath is required")
	}
	query := map[string]string{
		"filepath": path,
	}
	return c.http.DoJSON("DELETE", c.path("/vms/%d/file", vmID), query, nil, nil)
}

func (c *WhiteSkyClient) ReadVMFile(vmID int, filepath string, size, seek int) (string, error) {
	path := strings.TrimSpace(filepath)
	if path == "" {
		return "", fmt.Errorf("filepath is required")
	}
	if size <= 0 {
		size = 2 * 1024 * 1024
	}
	if size > 2*1024*1024 {
		size = 2 * 1024 * 1024
	}
	if seek < 0 {
		seek = 0
	}

	query := map[string]string{
		"filepath": path,
		"size":     strconv.Itoa(size),
		"seek":     strconv.Itoa(seek),
	}
	var resp struct {
		Result string `json:"result"`
	}
	if err := c.http.DoJSON("GET", c.path("/vms/%d/file", vmID), query, nil, &resp); err != nil {
		return "", err
	}
	return resp.Result, nil
}

func (c *WhiteSkyClient) path(format string, args ...interface{}) string {
	suffix := format
	if len(args) > 0 {
		suffix = fmt.Sprintf(format, args...)
	}
	return fmt.Sprintf("/customers/%s/cloudspaces/%s%s", c.customerID, c.cloudspace, suffix)
}

func dedupeDomainStrings(values []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(values))
	for _, v := range values {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func IsSystemProvidedTopLevelDomain(domain, vcoDomain string) bool {
	d := strings.ToLower(strings.Trim(strings.TrimSpace(domain), "."))
	vco := strings.ToLower(strings.Trim(strings.TrimSpace(vcoDomain), "."))
	if d == "" {
		return false
	}
	if vco != "" && d == vco {
		return true
	}
	// Whitesky-generated system TLD pattern.
	if strings.HasSuffix(d, ".try-dns.whitesky.cloud") {
		return true
	}
	return false
}
