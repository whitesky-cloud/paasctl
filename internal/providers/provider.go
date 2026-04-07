package providers

import (
	"paasctl/internal/clients"
	"paasctl/internal/deployments"
	"time"
)

const ElestioName = "elestio"

type ProviderInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func Supported() []ProviderInfo {
	return []ProviderInfo{
		{
			Name:        ElestioName,
			Description: "Elestio BYOVM PaaS services",
		},
	}
}

type DeployTarget struct {
	Name            string
	TemplateID      int
	TemplateVersion string
	Location        string
	PublicIPv4      string
	SSHUser         string
	SSHPort         int
	VCPUs           int
	MemoryMiB       int
	StorageGiB      int
}

type ProvisionedService struct {
	ProjectID string
	ServiceID string
}

type Provider interface {
	Name() string
	Validate() error
	ListTemplates() ([]clients.TemplateSpec, error)
	GetTemplateSpec(templateID int) (clients.TemplateSpec, error)
	DefaultBootstrapCommand() string
	ProvisionService(target DeployTarget) (ProvisionedService, error)
	WaitUntilReady(dep deployments.Deployment, timeout time.Duration, progress func(string)) error
	AddDomain(dep deployments.Deployment, domain string) error
	DeleteService(dep deployments.Deployment, permanent bool) error
	LiveDomains(dep deployments.Deployment) ([]string, error)
}
