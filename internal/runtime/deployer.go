package runtime

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"paasctl/internal/clients"
	"paasctl/internal/deployments"
	"paasctl/internal/providers"
)

const vmAgentReadyTimeout = 10 * time.Minute

type DeployOptions struct {
	Name             string
	TemplateID       int
	VCPUs            int
	MemoryMiB        int
	ImageID          int
	DiskSizeGiB      int
	SSHPortPublic    int
	BootstrapCommand string
	AdditionalPorts  []int
	InfraTimeout     time.Duration
	ProviderTimeout  time.Duration
}

type PortMapping struct {
	LocalPort  int
	PublicPort int
}

type DeployPlan struct {
	Name                string
	Provider            string
	TemplateID          int
	TemplateName        string
	TemplateVersion     string
	ImageID             int
	ImageName           string
	ImageOSName         string
	Location            string
	VCPUs               int
	MemoryMiB           int
	DiskSizeGiB         int
	BootstrapCommand    string
	PortMappings        []PortMapping
	InfraTimeout        time.Duration
	ProviderTimeout     time.Duration
	PublicIPAddress     string
	ExternalNetworkID   string
	ExternalNetworkIP   string
	ExternalNetworkType string
}

type Deployer struct {
	WhiteSky *clients.WhiteSkyClient
	Provider providers.Provider
	Store    *deployments.Store
}

func (d *Deployer) BuildPlan(opts DeployOptions, progress func(string)) (DeployPlan, error) {
	step(progress, fmt.Sprintf("Fetching %s template metadata", d.Provider.Name()))
	tpl, err := d.Provider.GetTemplateSpec(opts.TemplateID)
	if err != nil {
		return DeployPlan{}, fmt.Errorf("unable to get %s template spec: %w", d.Provider.Name(), err)
	}

	bootstrap := strings.TrimSpace(opts.BootstrapCommand)
	if bootstrap == "" {
		bootstrap = strings.TrimSpace(tpl.BootstrapCommand)
	}
	if bootstrap == "" {
		bootstrap = d.Provider.DefaultBootstrapCommand()
	}

	ports := mergePorts(tpl.Ports, opts.AdditionalPorts)
	ports = ensurePort(ports, 22)

	sshPublicPort := opts.SSHPortPublic
	if sshPublicPort <= 0 {
		sshPublicPort = 22
	}

	mappings := make([]PortMapping, 0, len(ports))
	requiredPublicPorts := make([]int, 0, len(ports))
	for _, local := range ports {
		pub := local
		if local == 22 {
			pub = sshPublicPort
		}
		mappings = append(mappings, PortMapping{LocalPort: local, PublicPort: pub})
		requiredPublicPorts = append(requiredPublicPorts, pub)
	}

	step(progress, "Reading cloudspace info")
	cloudspace, err := d.WhiteSky.GetCloudspaceInfo()
	if err != nil {
		return DeployPlan{}, fmt.Errorf("failed to read cloudspace info: %w", err)
	}
	if cloudspace.Location == "" {
		return DeployPlan{}, fmt.Errorf("cloudspace location is empty")
	}
	if cloudspace.ExternalNetworkIP == "" {
		return DeployPlan{}, fmt.Errorf("cloudspace has no primary external network ip")
	}

	infraTimeout := opts.InfraTimeout
	if infraTimeout <= 0 {
		infraTimeout = 10 * time.Minute
	}

	providerTimeout := opts.ProviderTimeout
	if providerTimeout <= 0 {
		providerTimeout = 6 * time.Hour
	}

	imageID := opts.ImageID
	imageName := ""
	imageOSName := ""
	if imageID <= 0 {
		step(progress, "Selecting Ubuntu image in cloudspace location")
		images, err := d.WhiteSky.ListLocationImages(cloudspace.Location)
		if err != nil {
			return DeployPlan{}, fmt.Errorf("failed to list VM images for location %s: %w", cloudspace.Location, err)
		}
		selected, err := pickUbuntuImage(images)
		if err != nil {
			return DeployPlan{}, err
		}
		imageID = selected.ImageID
		imageName = selected.Name
		imageOSName = selected.OSName
	}

	step(progress, "Checking if all required ports are available")
	conflicts, err := d.checkPublicPortsAvailable(requiredPublicPorts, cloudspace.ExternalNetworkIP)
	if err != nil {
		return DeployPlan{}, err
	}

	plan := DeployPlan{
		Name:             opts.Name,
		Provider:         d.Provider.Name(),
		TemplateID:       tpl.TemplateID,
		TemplateName:     tpl.TemplateName,
		TemplateVersion:  tpl.TemplateVersion,
		ImageID:          imageID,
		ImageName:        imageName,
		ImageOSName:      imageOSName,
		Location:         cloudspace.Location,
		VCPUs:            opts.VCPUs,
		MemoryMiB:        opts.MemoryMiB,
		DiskSizeGiB:      opts.DiskSizeGiB,
		BootstrapCommand: bootstrap,
		PortMappings:     mappings,
		InfraTimeout:     infraTimeout,
		ProviderTimeout:  providerTimeout,
		PublicIPAddress:  cloudspace.ExternalNetworkIP,
	}

	if len(conflicts) == 0 {
		return plan, nil
	}

	step(progress, "Port conflicts found on primary public IP; searching alternative external IP")
	candidate, pickErr := d.findAlternativePublicIP(cloudspace.Location, requiredPublicPorts, cloudspace.ExternalNetworkIP)
	if pickErr != nil {
		return DeployPlan{}, fmt.Errorf("required public ports are not available on %s (%s); and no alternative external IP could be planned: %w", cloudspace.ExternalNetworkIP, strings.Join(conflicts, " | "), pickErr)
	}
	plan.PublicIPAddress = candidate.ExternalNetworkIP
	plan.ExternalNetworkID = candidate.ExternalNetworkID
	plan.ExternalNetworkIP = candidate.ExternalNetworkIP
	plan.ExternalNetworkType = candidate.Type
	return plan, nil
}

func (d *Deployer) Deploy(plan DeployPlan, progress func(string)) (dep deployments.Deployment, err error) {
	type rollbackAction struct {
		name string
		fn   func() error
	}
	rollbacks := make([]rollbackAction, 0)

	runRollback := func(cause error) error {
		step(progress, fmt.Sprintf("Deployment failed (%v). Starting rollback...", cause))
		var rollbackFailures []string
		for i := len(rollbacks) - 1; i >= 0; i-- {
			action := rollbacks[i]
			step(progress, "Rollback: "+action.name)
			if rbErr := action.fn(); rbErr != nil {
				rollbackFailures = append(rollbackFailures, fmt.Sprintf("%s: %v", action.name, rbErr))
			}
		}
		if len(rollbackFailures) > 0 {
			return fmt.Errorf("%v; rollback failures: %s", cause, strings.Join(rollbackFailures, " | "))
		}
		step(progress, "Rollback completed")
		return cause
	}

	step(progress, fmt.Sprintf("Creating VM with image ID %d", plan.ImageID))
	vmID, createErr := d.WhiteSky.CreateVM(clients.CreateVMRequest{
		Name:        plan.Name,
		Description: fmt.Sprintf("PaaS deployment (%s) managed by paasctl", plan.TemplateName),
		VCPUs:       plan.VCPUs,
		MemoryMiB:   plan.MemoryMiB,
		ImageID:     plan.ImageID,
		DiskSizeGiB: plan.DiskSizeGiB,
	})
	if createErr != nil {
		return dep, fmt.Errorf("create VM failed: %w", createErr)
	}
	rollbacks = append(rollbacks, rollbackAction{
		name: "delete VM",
		fn: func() error {
			return d.WhiteSky.DeleteVM(vmID, true)
		},
	})

	if plan.ExternalNetworkID != "" && plan.ExternalNetworkIP != "" {
		step(progress, fmt.Sprintf("Adding external network IP %s to cloudspace", plan.ExternalNetworkIP))
		added, addErr := d.WhiteSky.AddCloudspaceExternalNetwork(plan.ExternalNetworkID, nonEmpty(plan.ExternalNetworkType, "external"), plan.ExternalNetworkIP)
		if addErr != nil {
			return dep, runRollback(fmt.Errorf("failed to add cloudspace external network ip: %w", addErr))
		}
		plan.ExternalNetworkID = added.ExternalNetworkID
		plan.ExternalNetworkIP = added.ExternalNetworkIP
		rollbacks = append(rollbacks, rollbackAction{
			name: "remove added cloudspace external network",
			fn: func() error {
				return d.WhiteSky.RemoveCloudspaceExternalNetwork(plan.ExternalNetworkID, plan.ExternalNetworkIP)
			},
		})
	}

	step(progress, "Waiting for VM private IP")
	privateIP, ipErr := d.waitForVMPrivateIP(vmID, 2*time.Minute)
	if ipErr != nil {
		return dep, runRollback(ipErr)
	}

	serverPoolName := fmt.Sprintf("paasctl-%s", plan.Name)
	step(progress, "Creating ingress server pool")
	serverPoolID, spErr := d.WhiteSky.CreateServerPool(serverPoolName, "Managed by paasctl")
	if spErr != nil {
		return dep, runRollback(fmt.Errorf("create server pool failed: %w", spErr))
	}
	rollbacks = append(rollbacks, rollbackAction{
		name: "delete server pool",
		fn: func() error {
			return d.WhiteSky.DeleteServerPool(serverPoolID)
		},
	})

	step(progress, fmt.Sprintf("Adding VM host %s to server pool", privateIP))
	hostID, hostErr := d.WhiteSky.AddHostToServerPool(serverPoolID, privateIP)
	if hostErr != nil {
		return dep, runRollback(fmt.Errorf("add host to server pool failed: %w", hostErr))
	}
	rollbacks = append(rollbacks, rollbackAction{
		name: "remove host from server pool",
		fn: func() error {
			return d.WhiteSky.RemoveHostFromServerPool(serverPoolID, hostID)
		},
	})

	step(progress, "Creating TCP load balancers")
	lbRefs := make([]deployments.LoadBalancerRef, 0, len(plan.PortMappings))
	for _, pm := range plan.PortMappings {
		lbName := fmt.Sprintf("paasctl-%s-%d", plan.Name, pm.PublicPort)
		lbID, lbErr := d.WhiteSky.CreateTCPLoadBalancer(lbName, "Managed by paasctl", serverPoolID, pm.PublicPort, pm.LocalPort, plan.PublicIPAddress)
		if lbErr != nil {
			return dep, runRollback(fmt.Errorf("create load balancer for %d->%d failed: %w", pm.PublicPort, pm.LocalPort, lbErr))
		}
		lbIDCopy := lbID
		rollbacks = append(rollbacks, rollbackAction{
			name: fmt.Sprintf("delete load balancer %s", lbName),
			fn: func() error {
				return d.WhiteSky.DeleteLoadBalancer(lbIDCopy)
			},
		})
		lbRefs = append(lbRefs, deployments.LoadBalancerRef{
			ID:         lbID,
			Name:       lbName,
			LocalPort:  pm.LocalPort,
			PublicPort: pm.PublicPort,
			Protocol:   "tcp",
		})
	}

	sshPublicPort := plan.portForLocal(22)
	step(progress, fmt.Sprintf("Waiting for SSH reachability on %s:%d", plan.PublicIPAddress, sshPublicPort))
	if waitErr := WaitTCP(plan.PublicIPAddress, sshPublicPort, plan.InfraTimeout); waitErr != nil {
		return dep, runRollback(fmt.Errorf("ssh did not become reachable in time (%s:%d): %w", plan.PublicIPAddress, sshPublicPort, waitErr))
	}

	step(progress, "Waiting for VM QEMU agent to become ready")
	if execErr := d.waitForVMAgentAndRunBootstrap(vmID, plan.BootstrapCommand, vmAgentReadyTimeout); execErr != nil {
		return dep, runRollback(fmt.Errorf("failed to run bootstrap command in VM: %w", execErr))
	}

	step(progress, fmt.Sprintf("Triggering %s app/service deployment", d.Provider.Name()))
	service, createErr := d.Provider.ProvisionService(providers.DeployTarget{
		Name:            plan.Name,
		TemplateID:      plan.TemplateID,
		TemplateVersion: plan.TemplateVersion,
		Location:        plan.Location,
		PublicIPv4:      plan.PublicIPAddress,
		SSHUser:         "root",
		SSHPort:         sshPublicPort,
		VCPUs:           plan.VCPUs,
		MemoryMiB:       plan.MemoryMiB,
		StorageGiB:      plan.DiskSizeGiB,
	})
	if createErr != nil {
		return dep, runRollback(createErr)
	}

	step(progress, "Selecting preferred customer domain suffix")
	tld, tldErr := d.WhiteSky.SelectPreferredTopLevelDomain()
	if tldErr != nil {
		return dep, runRollback(fmt.Errorf("failed to select customer top-level domain: %w", tldErr))
	}
	customDomain := buildServiceDomain(plan.Name, tld)

	step(progress, fmt.Sprintf("Adding domain %s to whitesky.cloud DNS", customDomain))
	if dnsErr := d.WhiteSky.AddVMExternalNICDomain(vmID, plan.PublicIPAddress, customDomain); dnsErr != nil {
		return dep, runRollback(fmt.Errorf("failed to add domain to whitesky.cloud DNS: %w", dnsErr))
	}

	step(progress, fmt.Sprintf("Adding domain %s to %s service", customDomain, d.Provider.Name()))
	providerDep := deployments.Deployment{
		Name:              plan.Name,
		Provider:          d.Provider.Name(),
		ProviderProjectID: service.ProjectID,
		ProviderServiceID: service.ServiceID,
	}
	if d.Provider.Name() == providers.ElestioName {
		providerDep.ElestioProjectID = service.ProjectID
		providerDep.ElestioServerID = service.ServiceID
	}
	if domainErr := d.Provider.AddDomain(providerDep, customDomain); domainErr != nil {
		return dep, runRollback(fmt.Errorf("failed to add domain to %s service: %w", d.Provider.Name(), domainErr))
	}

	step(progress, "Saving deployment metadata to cloudspace notes")
	dep = deployments.Deployment{
		Name:                plan.Name,
		Provider:            d.Provider.Name(),
		TemplateID:          plan.TemplateID,
		TemplateName:        plan.TemplateName,
		TemplateVersion:     plan.TemplateVersion,
		VMID:                vmID,
		BootstrapCommand:    plan.BootstrapCommand,
		ServerPoolID:        serverPoolID,
		ServerPoolHostID:    hostID,
		LoadBalancers:       lbRefs,
		PublicIPAddress:     plan.PublicIPAddress,
		ExternalNetworkID:   plan.ExternalNetworkID,
		ExternalNetworkIP:   plan.ExternalNetworkIP,
		ExternalNetworkType: plan.ExternalNetworkType,
		ProviderProjectID:   service.ProjectID,
		ProviderServiceID:   service.ServiceID,
		Domain:              customDomain,
	}
	if d.Provider.Name() == providers.ElestioName {
		dep.ElestioProjectID = service.ProjectID
		dep.ElestioServerID = service.ServiceID
	}
	if saveErr := d.Store.Save(dep); saveErr != nil {
		return dep, runRollback(fmt.Errorf("failed to save deployment metadata note: %w", saveErr))
	}

	step(progress, fmt.Sprintf("Waiting for %s service deployment to complete", d.Provider.Name()))
	if waitErr := d.Provider.WaitUntilReady(dep, plan.ProviderTimeout, progress); waitErr != nil {
		return dep, fmt.Errorf("%s service did not become ready within %s: %w", d.Provider.Name(), plan.ProviderTimeout, waitErr)
	}

	return dep, nil
}

func (d *Deployer) waitForVMPrivateIP(vmID int, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		vm, err := d.WhiteSky.GetVM(vmID)
		if err == nil {
			if ip := privateIPv4FromVM(vm); ip != "" {
				return ip, nil
			}
		}
		time.Sleep(5 * time.Second)
	}
	return "", fmt.Errorf("timeout after %s while waiting for VM private IP", timeout)
}

func (d *Deployer) waitForVMAgentAndRunBootstrap(vmID int, command string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		_, err := d.WhiteSky.ExecCommand(vmID, command)
		if err == nil {
			return nil
		}
		if !isVMAgentNotRunningError(err) {
			return err
		}
		lastErr = err
		time.Sleep(5 * time.Second)
	}

	if lastErr != nil {
		return fmt.Errorf("timeout after %s waiting for VM agent to become ready: %w", timeout, lastErr)
	}
	return fmt.Errorf("timeout after %s waiting for VM agent to become ready", timeout)
}

func (d *Deployer) checkPublicPortsAvailable(required []int, targetIP string) ([]string, error) {
	requiredSet := make(map[int]bool)
	for _, p := range required {
		if p > 0 {
			requiredSet[p] = true
		}
	}

	conflicts := make([]string, 0)

	pfs, err := d.WhiteSky.ListPortForwards()
	if err != nil {
		return nil, fmt.Errorf("failed to check existing portforwards: %w", err)
	}
	for _, pf := range pfs {
		if requiredSet[pf.PublicPort] && ipMatches(targetIP, pf.PublicIP) {
			conflicts = append(conflicts, fmt.Sprintf("port %d already used by portforward %s", pf.PublicPort, pf.PortforwardID))
		}
	}

	lbs, err := d.WhiteSky.ListLoadBalancers()
	if err != nil {
		return nil, fmt.Errorf("failed to check existing load balancers: %w", err)
	}
	for _, lb := range lbs {
		full, getErr := d.WhiteSky.GetLoadBalancer(lb.LoadBalancerID)
		if getErr != nil {
			return nil, fmt.Errorf("failed to inspect load balancer %s: %w", lb.LoadBalancerID, getErr)
		}
		if requiredSet[full.FrontEnd.Port] && ipMatches(targetIP, full.FrontEnd.IPAddress) {
			conflicts = append(conflicts, fmt.Sprintf("port %d already used by load balancer %s", full.FrontEnd.Port, lb.LoadBalancerID))
		}
	}

	rps, err := d.WhiteSky.ListReverseProxies()
	if err != nil {
		return nil, fmt.Errorf("failed to check existing reverse proxies: %w", err)
	}
	for _, rp := range rps {
		full, getErr := d.WhiteSky.GetReverseProxy(rp.ReverseProxyID)
		if getErr != nil {
			return nil, fmt.Errorf("failed to inspect reverse proxy %s: %w", rp.ReverseProxyID, getErr)
		}
		if full.FrontEnd.HTTPPort > 0 && requiredSet[full.FrontEnd.HTTPPort] && ipMatches(targetIP, full.FrontEnd.IPAddress) {
			conflicts = append(conflicts, fmt.Sprintf("port %d already used by reverse proxy %s (http)", full.FrontEnd.HTTPPort, rp.ReverseProxyID))
		}
		if full.FrontEnd.HTTPSPort > 0 && requiredSet[full.FrontEnd.HTTPSPort] && ipMatches(targetIP, full.FrontEnd.IPAddress) {
			conflicts = append(conflicts, fmt.Sprintf("port %d already used by reverse proxy %s (https)", full.FrontEnd.HTTPSPort, rp.ReverseProxyID))
		}
	}

	sort.Strings(conflicts)
	return dedupeStrings(conflicts), nil
}

func (d *Deployer) findAlternativePublicIP(location string, requiredPublicPorts []int, excludeIP string) (clients.CloudspaceExternalNetwork, error) {
	attached, err := d.WhiteSky.ListCloudspaceExternalNetworks()
	if err != nil {
		return clients.CloudspaceExternalNetwork{}, fmt.Errorf("failed to list cloudspace external networks: %w", err)
	}
	attachedIPSet := make(map[string]bool)
	for _, item := range attached {
		if item.ExternalNetworkIP != "" {
			attachedIPSet[item.ExternalNetworkIP] = true
		}
	}

	networks, err := d.WhiteSky.ListLocationExternalNetworks(location)
	if err != nil {
		return clients.CloudspaceExternalNetwork{}, fmt.Errorf("failed to list location external networks: %w", err)
	}

	for _, nw := range networks {
		if !nw.Public {
			continue
		}
		ips, ipErr := d.WhiteSky.ListExternalNetworkAvailableIPs(location, nw.ExternalNetworkID)
		if ipErr != nil {
			continue
		}
		for _, ip := range ips {
			if attachedIPSet[ip] {
				continue
			}
			if ip == strings.TrimSpace(excludeIP) {
				continue
			}
			conflicts, cErr := d.checkPublicPortsAvailable(requiredPublicPorts, ip)
			if cErr != nil {
				continue
			}
			if len(conflicts) == 0 {
				return clients.CloudspaceExternalNetwork{
					ExternalNetworkID: strconv.Itoa(nw.ExternalNetworkID),
					ExternalNetworkIP: ip,
					Type:              "external",
				}, nil
			}
		}
	}

	return clients.CloudspaceExternalNetwork{}, fmt.Errorf("no alternative public external IP found with free required ports")
}

func ipMatches(targetIP, existingIP string) bool {
	t := strings.TrimSpace(targetIP)
	e := strings.TrimSpace(existingIP)
	if e == "" || t == "" {
		return true
	}
	return t == e
}

func dedupeStrings(values []string) []string {
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

func privateIPv4FromVM(vm clients.VMInfo) string {
	fallback := ""
	for _, nic := range vm.NetworkInterfaces {
		ip := strings.TrimSpace(nic.IPAddress)
		parsed := net.ParseIP(ip)
		if parsed == nil || parsed.To4() == nil {
			continue
		}
		if isRFC1918(parsed.String()) {
			return parsed.String()
		}
		if fallback == "" {
			fallback = parsed.String()
		}
	}
	return fallback
}

func isRFC1918(ip string) bool {
	return strings.HasPrefix(ip, "10.") || strings.HasPrefix(ip, "192.168.") || has172PrivatePrefix(ip)
}

func has172PrivatePrefix(ip string) bool {
	parts := strings.Split(ip, ".")
	if len(parts) < 2 || parts[0] != "172" {
		return false
	}
	second := toInt(parts[1])
	return second >= 16 && second <= 31
}

func toInt(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func (p DeployPlan) portForLocal(local int) int {
	for _, pm := range p.PortMappings {
		if pm.LocalPort == local {
			return pm.PublicPort
		}
	}
	return local
}

func step(progress func(string), message string) {
	if progress != nil {
		progress(message)
	}
}

func WaitTCP(host string, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	address := fmt.Sprintf("%s:%d", host, port)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 5*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("timeout after %s", timeout)
}

func waitForLoadBalancers(host string, lbs []deployments.LoadBalancerRef, timeout time.Duration, progress func(string)) error {
	if len(lbs) == 0 {
		return nil
	}

	type pendingLB struct {
		id         string
		publicPort int
		localPort  int
	}

	pending := make(map[int]pendingLB)
	for _, lb := range lbs {
		if lb.PublicPort <= 0 {
			continue
		}
		if _, exists := pending[lb.PublicPort]; exists {
			continue
		}
		pending[lb.PublicPort] = pendingLB{
			id:         lb.ID,
			publicPort: lb.PublicPort,
			localPort:  lb.LocalPort,
		}
	}
	if len(pending) == 0 {
		return nil
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ports := make([]int, 0, len(pending))
		for port := range pending {
			ports = append(ports, port)
		}
		sort.Ints(ports)

		for _, port := range ports {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 2*time.Second)
			if err != nil {
				continue
			}
			_ = conn.Close()
			lb := pending[port]
			announceLoadBalancerReady(progress, lb.publicPort)
			delete(pending, port)
		}

		if len(pending) == 0 {
			return nil
		}
		time.Sleep(2 * time.Second)
	}

	ports := make([]string, 0, len(pending))
	for _, lb := range pending {
		ports = append(ports, fmt.Sprintf("%d", lb.publicPort))
	}
	sort.Strings(ports)
	return fmt.Errorf("timed out waiting for public ports %s on %s", strings.Join(ports, ", "), host)
}

func announceLoadBalancerWait(progress func(string), host string, lbs []deployments.LoadBalancerRef) {
	if progress == nil || len(lbs) == 0 {
		return
	}

	parts := make([]string, 0, len(lbs))
	for _, lb := range lbs {
		if lb.PublicPort <= 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s,%d,%d", lb.ID, lb.LocalPort, lb.PublicPort))
	}
	if len(parts) == 0 {
		return
	}
	progress("__lbwait_start__|" + host + "|" + strings.Join(parts, ";"))
}

func announceLoadBalancerReady(progress func(string), publicPort int) {
	if progress == nil || publicPort <= 0 {
		return
	}
	progress(fmt.Sprintf("__lbwait_ready__|%d", publicPort))
}

func pickUbuntuImage(images []clients.VMImage) (clients.VMImage, error) {
	for _, version := range []string{"24.04", "22.04"} {
		for _, img := range images {
			combined := strings.ToLower(strings.TrimSpace(img.Name + " " + img.OSName))
			if strings.Contains(combined, "ubuntu") && strings.Contains(combined, version) {
				return img, nil
			}
		}
	}
	return clients.VMImage{}, fmt.Errorf("no suitable Ubuntu image found in whitesky.cloud location (expected Ubuntu 24.04 or 22.04)")
}

func mergePorts(base []int, extra []int) []int {
	set := make(map[int]bool)
	for _, p := range base {
		if p > 0 {
			set[p] = true
		}
	}
	for _, p := range extra {
		if p > 0 {
			set[p] = true
		}
	}
	out := make([]int, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

func ensurePort(ports []int, port int) []int {
	for _, p := range ports {
		if p == port {
			return ports
		}
	}
	ports = append(ports, port)
	sort.Ints(ports)
	return ports
}

func nonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func isVMAgentNotRunningError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "vm agent must be enabled and running") || strings.Contains(msg, "agent status: not_running")
}

func buildServiceDomain(name, tld string) string {
	label := sanitizeDomainLabel(name)
	suffix := strings.Trim(strings.ToLower(strings.TrimSpace(tld)), ".")
	if label == "" {
		label = "app"
	}
	if suffix == "" {
		return label
	}
	return label + "." + suffix
}

func sanitizeDomainLabel(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return ""
	}

	var b strings.Builder
	lastDash := false
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
