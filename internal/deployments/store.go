package deployments

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"paasctl/internal/clients"
)

const NoteTitlePrefix = "paasctl:deployment:"

type PortForwardRef struct {
	ID         string `json:"id"`
	LocalPort  int    `json:"local_port"`
	PublicPort int    `json:"public_port"`
	PublicIP   string `json:"public_ip"`
	Protocol   string `json:"protocol"`
}

type LoadBalancerRef struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	LocalPort  int    `json:"local_port"`
	PublicPort int    `json:"public_port"`
	Protocol   string `json:"protocol"`
}

type Deployment struct {
	Name                string            `json:"name"`
	Provider            string            `json:"provider"`
	TemplateID          int               `json:"template_id"`
	TemplateName        string            `json:"template_name"`
	TemplateVersion     string            `json:"template_version"`
	VMID                int               `json:"vm_id"`
	BootstrapCommand    string            `json:"bootstrap_command"`
	PortForwards        []PortForwardRef  `json:"port_forwards"`
	ServerPoolID        string            `json:"server_pool_id"`
	ServerPoolHostID    string            `json:"server_pool_host_id"`
	LoadBalancers       []LoadBalancerRef `json:"load_balancers"`
	PublicIPAddress     string            `json:"public_ip_address"`
	ExternalNetworkID   string            `json:"external_network_id"`
	ExternalNetworkIP   string            `json:"external_network_ip"`
	ExternalNetworkType string            `json:"external_network_type"`
	ProviderProjectID   string            `json:"provider_project_id,omitempty"`
	ProviderServiceID   string            `json:"provider_service_id,omitempty"`
	ElestioProjectID    string            `json:"elestio_project_id,omitempty"`
	ElestioServerID     string            `json:"elestio_server_id,omitempty"`
	Domain              string            `json:"domain,omitempty"`
	CreatedAt           string            `json:"created_at"`
}

type StoredDeployment struct {
	NoteID string
	Deployment
}

type WhiteSkyNotes interface {
	ListNotes() ([]clients.Note, error)
	CreateNote(title, content string) error
	DeleteNote(noteID string) error
}

type Store struct {
	notes WhiteSkyNotes
}

func NewStore(notes WhiteSkyNotes) *Store {
	return &Store{notes: notes}
}

func (s *Store) Save(dep Deployment) error {
	if dep.CreatedAt == "" {
		dep.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	raw, err := json.Marshal(dep)
	if err != nil {
		return err
	}
	return s.notes.CreateNote(NoteTitlePrefix+dep.Name, string(raw))
}

func (s *Store) List() ([]StoredDeployment, error) {
	notes, err := s.notes.ListNotes()
	if err != nil {
		return nil, err
	}
	out := make([]StoredDeployment, 0)
	for _, note := range notes {
		if !strings.HasPrefix(note.Title, NoteTitlePrefix) {
			continue
		}
		var dep Deployment
		if err := json.Unmarshal([]byte(note.Content), &dep); err != nil {
			continue
		}
		out = append(out, StoredDeployment{
			NoteID:     note.ID,
			Deployment: dep,
		})
	}
	return out, nil
}

func (s *Store) FindByName(name string) (StoredDeployment, error) {
	items, err := s.List()
	if err != nil {
		return StoredDeployment{}, err
	}
	for _, item := range items {
		if item.Name == name {
			return item, nil
		}
	}
	return StoredDeployment{}, fmt.Errorf("deployment %q not found", name)
}

func (s *Store) Delete(noteID string) error {
	return s.notes.DeleteNote(noteID)
}
