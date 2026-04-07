package providers

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"paasctl/internal/clients"
	"paasctl/internal/deployments"
)

const defaultElestioBootstrapCommand = "curl https://raw.githubusercontent.com/elestio/byovm/main/prod.sh | sudo bash"

type ElestioConfig struct {
	BaseURL            string
	Email              string
	APIToken           string
	ProjectID          string
	BYOVMPricePerHour  float64
	BYOVMProviderLabel string
}

type ElestioProvider struct {
	client *clients.ElestioClient
	cfg    ElestioConfig
	jwtVal string
}

func NewElestioProvider(cfg ElestioConfig) *ElestioProvider {
	return &ElestioProvider{
		client: clients.NewElestioClient(clients.ElestioConfig{BaseURL: cfg.BaseURL}),
		cfg:    cfg,
	}
}

func (p *ElestioProvider) Name() string {
	return ElestioName
}

func (p *ElestioProvider) Validate() error {
	if p.cfg.Email == "" {
		return fmt.Errorf("missing required config PAASCTL_ELESTIO_EMAIL or paas-providers.elestio.email")
	}
	if p.cfg.APIToken == "" {
		return fmt.Errorf("missing required config PAASCTL_ELESTIO_API_TOKEN or paas-providers.elestio.api_token")
	}
	return nil
}

func (p *ElestioProvider) ListTemplates() ([]clients.TemplateSpec, error) {
	return p.client.ListTemplates()
}

func (p *ElestioProvider) GetTemplateSpec(templateID int) (clients.TemplateSpec, error) {
	return p.client.GetTemplateSpec(templateID)
}

func (p *ElestioProvider) DefaultBootstrapCommand() string {
	return defaultElestioBootstrapCommand
}

func (p *ElestioProvider) ProvisionService(target DeployTarget) (ProvisionedService, error) {
	jwt, err := p.jwt()
	if err != nil {
		return ProvisionedService{}, fmt.Errorf("failed to authenticate with elestio: %w", err)
	}

	projectID := strings.TrimSpace(p.cfg.ProjectID)
	if projectID == "" {
		projectID = clients.InferElestioProjectID(jwt)
	}
	if projectID == "" {
		return ProvisionedService{}, fmt.Errorf("could not determine elestio project id; set PAASCTL_ELESTIO_PROJECT_ID or paas-providers.elestio.project_id")
	}

	if err := p.client.TestBYOVMConnection(clients.TestBYOVMRequest{
		JWT:      jwt,
		UserName: nonEmpty(target.SSHUser, "root"),
		Port:     target.SSHPort,
		IPv4:     target.PublicIPv4,
	}); err != nil {
		return ProvisionedService{}, fmt.Errorf("elestio BYOVM connectivity test failed: %w", err)
	}

	createResp, err := p.client.CreateBYOVMService(clients.CreateBYOVMServiceRequest{
		JWT:                jwt,
		TemplateID:         target.TemplateID,
		TemplateVersion:    target.TemplateVersion,
		ServerName:         target.Name,
		AdminEmail:         p.cfg.Email,
		ProjectID:          projectID,
		Datacenter:         target.Location,
		ProviderCustomName: nonEmpty(p.cfg.BYOVMProviderLabel, "whitesky.cloud"),
		PublicIPv4:         target.PublicIPv4,
		SSHUser:            nonEmpty(target.SSHUser, "root"),
		SSHPort:            target.SSHPort,
		PricePerHour:       p.cfg.BYOVMPricePerHour,
		VCPUs:              target.VCPUs,
		RAMGiB:             memoryMiBToGiB(target.MemoryMiB),
		StorageGiB:         target.StorageGiB,
		ArchitectureType:   "amd64",
	})
	if err != nil {
		return ProvisionedService{}, fmt.Errorf("elestio createServer failed: %w", err)
	}

	serviceID := extractElestioServerID(createResp)
	if serviceID == "" {
		return ProvisionedService{}, fmt.Errorf("elestio createServer response did not include server identifier")
	}

	return ProvisionedService{ProjectID: projectID, ServiceID: serviceID}, nil
}

func (p *ElestioProvider) WaitUntilReady(dep deployments.Deployment, timeout time.Duration, progress func(string)) error {
	projectID := providerProjectID(dep)
	if projectID == "" {
		return fmt.Errorf("missing Elestio project ID in stored deployment metadata")
	}
	jwt, err := p.jwt()
	if err != nil {
		return fmt.Errorf("failed to authenticate to elestio: %w", err)
	}
	serviceID, err := p.resolveServiceID(jwt, dep)
	if err != nil {
		return err
	}

	deadline := time.Now().Add(timeout)
	lastState := ""
	for time.Now().Before(deadline) {
		svc, err := p.client.GetServerDetails(jwt, projectID, serviceID)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}

		status := strings.TrimSpace(svc.Status)
		deploymentStatus := strings.TrimSpace(svc.DeploymentStatus)
		state := fmt.Sprintf("%s / %s", nonEmpty(status, "<unknown>"), nonEmpty(deploymentStatus, "<unknown>"))
		if state != lastState {
			lastState = state
			if progress != nil {
				progress(fmt.Sprintf("Elestio service status: %s", state))
			}
		}

		if isElestioServiceReady(svc) {
			return nil
		}
		if isElestioServiceFailed(svc) {
			return fmt.Errorf("elestio service entered failed state: %s", state)
		}

		time.Sleep(5 * time.Second)
	}

	if lastState != "" {
		return fmt.Errorf("timeout after %s while waiting for elestio service readiness (last status: %s)", timeout, lastState)
	}
	return fmt.Errorf("timeout after %s while waiting for elestio service readiness", timeout)
}

func (p *ElestioProvider) AddDomain(dep deployments.Deployment, domain string) error {
	jwt, err := p.jwt()
	if err != nil {
		return fmt.Errorf("failed to authenticate to elestio: %w", err)
	}
	serviceID, err := p.resolveServiceID(jwt, dep)
	if err != nil {
		return err
	}
	return p.client.AddSSLDomain(jwt, serviceID, domain)
}

func (p *ElestioProvider) DeleteService(dep deployments.Deployment, permanent bool) error {
	projectID := providerProjectID(dep)
	if projectID == "" {
		return fmt.Errorf("missing Elestio server ID or project ID in stored deployment metadata")
	}
	jwt, err := p.jwt()
	if err != nil {
		return fmt.Errorf("failed to authenticate to Elestio for cleanup: %w", err)
	}
	serviceID, err := p.resolveServiceID(jwt, dep)
	if err != nil {
		return err
	}
	return p.client.DeleteServer(jwt, projectID, serviceID, permanent)
}

func (p *ElestioProvider) LiveDomains(dep deployments.Deployment) ([]string, error) {
	projectID := providerProjectID(dep)
	if projectID == "" {
		return nil, fmt.Errorf("missing Elestio server ID or project ID in stored deployment metadata")
	}
	jwt, err := p.jwt()
	if err != nil {
		return nil, err
	}
	serviceID, err := p.resolveServiceID(jwt, dep)
	if err != nil {
		return nil, err
	}

	set := make(map[string]bool)
	if domains, err := p.client.ListSSLDomains(jwt, serviceID); err == nil {
		for _, d := range domains {
			if strings.TrimSpace(d) != "" {
				set[strings.TrimSpace(d)] = true
			}
		}
	}
	if domains, err := p.client.GetServerReachableDomains(jwt, projectID, serviceID); err == nil {
		for _, d := range domains {
			if strings.TrimSpace(d) != "" {
				set[strings.TrimSpace(d)] = true
			}
		}
	}
	if len(set) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}

func (p *ElestioProvider) resolveServiceID(jwt string, dep deployments.Deployment) (string, error) {
	serviceID := providerServiceID(dep)
	if serviceID != "" {
		return serviceID, nil
	}

	projectID := providerProjectID(dep)
	if projectID != "" && strings.TrimSpace(dep.Name) != "" {
		if serviceID := p.findServiceIDByName(jwt, projectID, dep); serviceID != "" {
			return serviceID, nil
		}
	}
	return "", fmt.Errorf("deployment %q has no stored elestio server id", dep.Name)
}

func (p *ElestioProvider) findServiceIDByName(jwt, projectID string, dep deployments.Deployment) string {
	services, err := p.client.ListServices(jwt, projectID)
	if err != nil {
		return ""
	}

	name := strings.TrimSpace(dep.Name)
	publicIP := strings.TrimSpace(dep.PublicIPAddress)
	var firstNameMatch string
	for _, svc := range services {
		displayName := strings.TrimSpace(svc.DisplayName)
		serverName := strings.TrimSpace(svc.ServerName)
		if displayName != name && serverName != name {
			continue
		}
		vmID := strings.TrimSpace(svc.VMID)
		if vmID == "" {
			continue
		}
		if publicIP != "" && (strings.TrimSpace(svc.GlobalIP) == publicIP || strings.TrimSpace(svc.IPv4) == publicIP) {
			return vmID
		}
		if firstNameMatch == "" {
			firstNameMatch = vmID
		}
	}
	return firstNameMatch
}

func findElestioService(services []clients.ElestioServiceSummary, serviceID, name string) (clients.ElestioServiceSummary, bool) {
	trimmedServiceID := strings.TrimSpace(serviceID)
	trimmedName := strings.TrimSpace(name)
	for _, svc := range services {
		if trimmedServiceID != "" && strings.TrimSpace(svc.VMID) == trimmedServiceID {
			return svc, true
		}
		if trimmedServiceID != "" && strings.TrimSpace(svc.ID) == trimmedServiceID {
			return svc, true
		}
		if trimmedName != "" && (strings.TrimSpace(svc.DisplayName) == trimmedName || strings.TrimSpace(svc.ServerName) == trimmedName) {
			return svc, true
		}
	}
	return clients.ElestioServiceSummary{}, false
}

func isElestioServiceReady(svc clients.ElestioServiceSummary) bool {
	status := strings.ToLower(strings.TrimSpace(svc.Status))
	deploymentStatus := strings.ToLower(strings.TrimSpace(svc.DeploymentStatus))
	return status == "running" && deploymentStatus == "deployed"
}

func isElestioServiceFailed(svc clients.ElestioServiceSummary) bool {
	status := strings.ToLower(strings.TrimSpace(svc.Status))
	deploymentStatus := strings.ToLower(strings.TrimSpace(svc.DeploymentStatus))
	return status == "failed" || deploymentStatus == "failed" || deploymentStatus == "error"
}

func (p *ElestioProvider) jwt() (string, error) {
	if strings.TrimSpace(p.jwtVal) != "" {
		return p.jwtVal, nil
	}
	jwt, err := p.client.GetJWT(p.cfg.Email, p.cfg.APIToken)
	if err != nil {
		return "", err
	}
	p.jwtVal = jwt
	return jwt, nil
}

func providerProjectID(dep deployments.Deployment) string {
	if strings.TrimSpace(dep.ProviderProjectID) != "" {
		return strings.TrimSpace(dep.ProviderProjectID)
	}
	return strings.TrimSpace(dep.ElestioProjectID)
}

func providerServiceID(dep deployments.Deployment) string {
	if strings.TrimSpace(dep.ProviderServiceID) != "" {
		return strings.TrimSpace(dep.ProviderServiceID)
	}
	return strings.TrimSpace(dep.ElestioServerID)
}

func memoryMiBToGiB(memoryMiB int) int {
	if memoryMiB <= 0 {
		return 0
	}
	return int(math.Ceil(float64(memoryMiB) / 1024.0))
}

func extractElestioServerID(resp clients.ElestioActionResponse) string {
	for _, bucket := range []interface{}{resp.Result, resp.Data} {
		if id := extractPreferredElestioServerID(bucket); id != "" {
			return id
		}
	}
	return ""
}

func extractPreferredElestioServerID(v interface{}) string {
	switch t := v.(type) {
	case map[string]interface{}:
		for _, key := range []string{"providerServerID", "serverID", "serverId", "vmID", "serviceId", "serviceID"} {
			if raw, ok := t[key]; ok {
				if id := anyToString(raw); id != "" {
					return id
				}
			}
		}
		if raw, ok := t["resources"]; ok {
			if id := extractElestioServerIDFromResources(raw); id != "" {
				return id
			}
		}
		for _, value := range t {
			if id := extractPreferredElestioServerID(value); id != "" {
				return id
			}
		}
	case []interface{}:
		for _, item := range t {
			if id := extractPreferredElestioServerID(item); id != "" {
				return id
			}
		}
	}
	return ""
}

func extractElestioServerIDFromResources(v interface{}) string {
	items, ok := v.([]interface{})
	if !ok {
		return ""
	}
	for _, item := range items {
		resource, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(anyToString(resource["type"])), "server") {
			continue
		}
		if id := anyToString(resource["id"]); id != "" {
			return id
		}
	}
	return ""
}

func anyToString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.Itoa(int(t))
	default:
		return ""
	}
}

func nonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}
