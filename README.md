# paasctl

Golang CLI (Cobra) to deploy, list, and delete PaaS services/apps in whitesky.cloud VMs using external providers, starting with Elestio.

## Commands

- `paasctl config`
- `paasctl config path`
- `paasctl config keys`
- `paasctl config set <key> <value>`
- `paasctl config unset <key>`
- `paasctl config unlock`
- `paasctl config relock`
- `paasctl list cloudspaces`
- `paasctl list providers`
- `paasctl list deployments`
- `paasctl list tlds`
- `paasctl list templates --provider <provider> [--category <text>] [--search <text>]`
- `paasctl deploy --provider <provider> --name <name> --template-id <id> [flags]`
- `paasctl add domain --name <deployment-name>`
- `paasctl add storage --name <deployment-name> <size>` (example: `30G`)
- `paasctl add memory --name <deployment-name> <size>` (example: `512m`)
- `paasctl add vcpus --name <deployment-name> <count>` (example: `2`)
- `paasctl delete --name <name> [--permanent=true]`

Global option:
- `--log-api` logs all API requests and responses to stderr.
- `--json` outputs command results and errors as JSON.
- `--cloudspace` selects a whitesky.cloud cloudspace by configured name or direct cloudspace ID for commands that operate on a cloudspace.

For commands that normally prompt interactively, provide the required flags when using `--json` so stdout remains valid JSON. For deploys, combine `--json` with `--no-plan-approval`.

Provider option:
- `--provider` selects the PaaS provider. There is no default provider; run `paasctl list providers` to see supported providers.

## Configuration

Configuration can be provided through:
- Environment variables.
- YAML config file at `~/.config/paastctl/config.yaml`.

Environment variables override values from the YAML config file.

Sensitive YAML values are encrypted before being written:
- `whitesky.token`
- `paas-providers.elestio.api_token`

To encrypt or read those values from YAML, unlock config secrets first:

```bash
paasctl config unlock
```

`unlock` reads the config password from stdin with terminal echo disabled, derives a deterministic ssh-agent identity from that password, and adds it to `ssh-agent`. The encrypted YAML values are then decrypted through ssh-agent signatures without prompting on every CLI invocation.

To remove the unlock token from `ssh-agent`:

```bash
paasctl config relock
```

Use the CLI to manage the YAML file:

```bash
paasctl config path
paasctl config keys
paasctl config unlock
paasctl config set whitesky.token "..."
paasctl config set whitesky.customer_id cust-123
paasctl config set whitesky.iam_base_url https://<iam-domain>
paasctl config set whitesky.cloudspaces.prod.cloudspace_id cs-abc
paasctl config set whitesky.cloudspaces.dev.cloudspace_id cs-def
paasctl config set paas-providers.elestio.email "..."
paasctl config set paas-providers.elestio.api_token "..."
paasctl config unset paas-providers.elestio.project_id
paasctl config relock
```

Example YAML:

```yaml
whitesky:
  base_url: https://try.whitesky.cloud/api/1
  iam_base_url: https://<iam-domain>
  token: "enc:v1:..."
  customer_id: cust-123
  request_timeout: 300s
  cloudspaces:
    prod:
      cloudspace_id: cs-abc
    dev:
      cloudspace_id: cs-def

paas-providers:
  elestio:
    base_url: https://api.elest.io
    email: "..."
    api_token: "enc:v1:..."
    project_id: ""
    byovm_price_per_hour: 0
    byovm_provider_label: whitesky.cloud
```

## Required environment variables or YAML keys

### whitesky.cloud

- `PAASCTL_WHITESKY_TOKEN` or `whitesky.token`
- `PAASCTL_WHITESKY_CUSTOMER_ID` or `whitesky.customer_id`
- `PAASCTL_WHITESKY_CLOUDSPACE_ID`, `--cloudspace <id>`, or `whitesky.cloudspaces.<name>.cloudspace_id`

## Optional environment variables or YAML keys

- `PAASCTL_WHITESKY_BASE_URL` or `whitesky.base_url` (default: `https://try.whitesky.cloud/api/1`)
- `PAASCTL_WHITESKY_IAM_BASE_URL` or `whitesky.iam_base_url` (used to refresh expiring whitesky.cloud JWTs)
- `PAASCTL_WHITESKY_REQUEST_TIMEOUT` or `whitesky.request_timeout` (default: `300s`, minimum enforced: `300s`; set e.g. `24h` for long-running operations)
- `PAASCTL_ELESTIO_BASE_URL` or `paas-providers.elestio.base_url` (default: `https://api.elest.io`)
- `PAASCTL_ELESTIO_PROJECT_ID` or `paas-providers.elestio.project_id` (if omitted, the CLI tries to infer it from Elestio JWT claims)
- `PAASCTL_ELESTIO_BYOVM_PRICE_PER_HOUR` or `paas-providers.elestio.byovm_price_per_hour` (default: `0`)
- `PAASCTL_ELESTIO_BYOVM_PROVIDER_LABEL` or `paas-providers.elestio.byovm_provider_label` (default: `whitesky.cloud`)

## Required Elestio variables for provider operations

- `PAASCTL_ELESTIO_EMAIL` or `paas-providers.elestio.email`
- `PAASCTL_ELESTIO_API_TOKEN` or `paas-providers.elestio.api_token`

## Deploy flow

1. Fetches provider template metadata. For Elestio this calls `/api/servers/getTemplates` and extracts:
   - bootstrap/install command
   - exposed ports
2. If `--image-id` is not provided, queries whitesky.cloud images in the cloudspace location and auto-selects Ubuntu `24.04` (fallback `22.04`).
3. Checks required public ports before doing any changes (against existing portforwards, reverse proxies, and load balancers).
4. If required ports conflict on the cloudspace primary public IP, plans an extra external public IP and binds new port forwards to that IP.
5. Prints a deploy plan and asks for approval (unless `--no-plan-approval`).
6. Uses separate timeouts for infrastructure and provider readiness:
   `--timeout` defaults to `10m` for VM, SSH, and bootstrap readiness.
   `--provider-timeout` defaults to `6h` for long-running provider deployments such as Elestio application installation.
7. Creates a VM in whitesky.cloud.
8. Adds the extra external network IP when planned.
9. Creates direct TCP port forwards for the required ports.
10. Waits until the SSH endpoint is reachable through the port forward.
11. Executes bootstrap command inside the VM (`/vms/{vm_id}/exec`) to authorize provider SSH access.
12. Calls the provider implementation to create the service. For Elestio this authenticates, tests BYOVM connectivity (`/api/servers/getBYOVM`), and creates the service (`/api/servers/createServer`).
13. Selects a preferred customer top-level domain in whitesky.cloud (prefers customer-owned domain over VCO-provided domain when both exist).
14. Adds the generated `<deployment-name>.<tld>` domain to whitesky.cloud DNS (VM external NIC).
15. Adds the same domain to the provider service. For Elestio this uses `/api/servers/DoActionOnServer` with `SSLDomainsAdd`.
16. Stores deployment metadata in cloudspace notes (`paasctl:deployment:<name>`).
17. Waits until the provider reports the deployment is ready.
18. If any step fails, rolls back created whitesky.cloud resources.

## Notes-based metadata

All deployment state is stored in cloudspace notes. `list` and `delete` use these notes as the source of truth.

For provider-backed deployments, `delete` also attempts to remove the related provider service when `provider_service_id` is present in the stored note and provider credentials are configured. Existing Elestio notes using `elestio_server_id` remain supported.

## Example

```bash
export PAASCTL_WHITESKY_TOKEN="..."
export PAASCTL_WHITESKY_CUSTOMER_ID="cust-123"
export PAASCTL_WHITESKY_CLOUDSPACE_ID="cs-abc"
export PAASCTL_WHITESKY_REQUEST_TIMEOUT="300s"

# Build
go build -o paasctl .

# Show version
./paasctl version

# Deploy template id 14
./paasctl --cloudspace prod --log-api deploy --provider elestio --name wordpress-prod --template-id 14 --vcpus 2 --memory 4096 --ssh-public-port 2222

# Deploy without interactive approval prompt
./paasctl --cloudspace prod deploy --provider elestio --name wordpress-prod --template-id 14 --no-plan-approval

# Allow longer-running provider deployment readiness checks
./paasctl --cloudspace prod deploy --provider elestio --name wordpress-prod --template-id 14 --provider-timeout 12h

# Browse templates first
./paasctl list templates --provider elestio --search wordpress

# List
./paasctl --cloudspace prod list deployments

# Add domain after deployment
./paasctl --cloudspace prod add domain --name wordpress-prod

# Expand VM disk by 30 GiB and grow filesystems/LVM inside guest
./paasctl --cloudspace prod add storage --name wordpress-prod 30G

# Increase VM memory by 512 MiB
./paasctl --cloudspace prod add memory --name wordpress-prod 512m

# Increase VM CPU by 2 vCPUs
./paasctl --cloudspace prod add vcpus --name wordpress-prod 2

# Delete
./paasctl --cloudspace prod delete --name wordpress-prod --permanent=true
```

## Note about Elestio bootstrap command

If Elestio template metadata does not include a bootstrap command, the CLI uses this default command:

```bash
curl https://raw.githubusercontent.com/elestio/byovm/main/prod.sh | sudo bash
```

You can still override it per deploy:

```bash
./paasctl deploy --name myapp --template-id 14 --bootstrap-command "curl -fsSL https://example/bootstrap.sh | sh"
```
