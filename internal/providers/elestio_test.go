package providers

import (
	"testing"

	"paasctl/internal/clients"
)

func TestExtractElestioServerIDPrefersProviderServerIDOverActionID(t *testing.T) {
	resp := clients.ElestioActionResponse{
		Result: map[string]interface{}{
			"action": map[string]interface{}{
				"id": 402419563,
				"resources": []interface{}{
					map[string]interface{}{
						"id":   12345678,
						"type": "server",
					},
				},
			},
			"providerServerID": 12345678,
		},
	}

	got := extractElestioServerID(resp)
	if got != "12345678" {
		t.Fatalf("extractElestioServerID() = %q, want %q", got, "12345678")
	}
}

func TestExtractElestioServerIDFallsBackToServerResourceID(t *testing.T) {
	resp := clients.ElestioActionResponse{
		Result: map[string]interface{}{
			"action": map[string]interface{}{
				"id": 402419563,
				"resources": []interface{}{
					map[string]interface{}{
						"id":   87654321,
						"type": "server",
					},
				},
			},
		},
	}

	got := extractElestioServerID(resp)
	if got != "87654321" {
		t.Fatalf("extractElestioServerID() = %q, want %q", got, "87654321")
	}
}
