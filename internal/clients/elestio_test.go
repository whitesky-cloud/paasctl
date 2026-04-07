package clients

import "testing"

func TestParseElestioServiceDetailsPayload(t *testing.T) {
	payload := map[string]interface{}{
		"serviceInfos": []interface{}{
			map[string]interface{}{
				"id":               float64(538901),
				"status":           "running",
				"deploymentStatus": "Deployed",
				"displayName":      "mydb",
				"providerServerID": "byovm-47182-1775652094504",
				"vmID":             "byovm-47182-1775652094504",
				"projectID":        "51626",
				"ipv4":             "185.69.164.154",
				"globalIP":         "10.63.21.1",
				"cname":            "mydb-u47182.vm.elestio.app",
			},
		},
	}

	got, err := parseElestioServiceDetailsPayload(payload)
	if err != nil {
		t.Fatalf("parseElestioServiceDetailsPayload() error = %v", err)
	}

	if got.ID != "538901" {
		t.Fatalf("parseElestioServiceDetailsPayload().ID = %q, want %q", got.ID, "538901")
	}
	if got.VMID != "byovm-47182-1775652094504" {
		t.Fatalf("parseElestioServiceDetailsPayload().VMID = %q, want provider server id", got.VMID)
	}
	if got.Status != "running" || got.DeploymentStatus != "Deployed" {
		t.Fatalf("parseElestioServiceDetailsPayload() status = %q / %q, want running / Deployed", got.Status, got.DeploymentStatus)
	}
}
