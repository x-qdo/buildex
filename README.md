# BuildEx

BuildEx is a CLI tool that manages Docker buildx workers on EC2 Spot instances for on-demand, cost-effective multi-architecture builds. It automatically provisions EC2 instances, configures them as Docker buildx workers, runs your builds, and cleans up resources when done.

## 🚀 Features

- **On-demand EC2 workers**: Spawn EC2 instances (spot or regular) as needed for builds
- **Multi-architecture builds**: Build for multiple architectures simultaneously (linux/amd64, linux/arm64, etc.)
- **Automatic setup**: Handles SSH key creation, security groups, and Docker configuration
- **Persistent mode**: Option to keep workers running for multiple builds

## 📋 Prerequisites

- Go 1.24 or later
- Docker with buildx enabled
- AWS CLI configured with appropriate permissions
- SSH client

### Required AWS Permissions

```json
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Effect": "Allow",
            "Action": [
                "ec2:RunInstances",
                "ec2:TerminateInstances",
                "ec2:DescribeInstances",
                "ec2:DescribeImages",
                "ec2:DescribeKeyPairs",
                "ec2:DescribeSecurityGroups",
                "ec2:DescribeSpotInstanceRequests",
                "ec2:RequestSpotInstances",
                "ec2:CreateKeyPair",
                "ec2:ImportKeyPair",
                "ec2:DeleteKeyPair",
                "ec2:CreateSecurityGroup",
                "ec2:AuthorizeSecurityGroupIngress",
                "ec2:CreateTags"
            ],
            "Resource": "*"
        }
    ]
}
```

## 🛠 Installation

### Using Go Install

```bash
go install github.com/x-qdo/buildex/cmd/buildex@latest
```

### Download Binary

Download the latest release from [GitHub Releases](https://github.com/x-qdo/buildex/releases):

```bash
# Linux AMD64
curl -L https://github.com/x-qdo/buildex/releases/latest/download/buildex-linux-amd64.tar.gz | tar xz
sudo mv buildex /usr/local/bin/

# macOS ARM64
curl -L https://github.com/x-qdo/buildex/releases/latest/download/buildex-darwin-arm64.tar.gz | tar xz
sudo mv buildex /usr/local/bin/
```

### Build from Source

```bash
git clone https://github.com/x-qdo/buildex.git
cd buildex
make build
sudo cp bin/buildex /usr/local/bin/
```

## 🚦 Quick Start

### 1. Configure AWS Credentials

```bash
aws configure
```

### 2. Basic Multi-Architecture Build

```bash
# Build for AMD64 and ARM64 using spot instances
buildex build . --platform linux/amd64,linux/arm64 --spot --tag myapp:latest
```

### 3. Start Persistent Workers

```bash
# Start workers for later use
buildex start --platform linux/amd64,linux/arm64 --spot

# Check status
buildex status

# Use existing workers for builds (automatic by default)
buildex build . --tag myapp:latest

# Stop workers when done
buildex stop --all
```

### 4. Using Existing Workers

BuildEx automatically detects and uses existing workers when available:

```bash
# Start workers once
buildex start --platform linux/amd64,linux/arm64 --spot

# Run multiple builds using the same workers
buildex build . --tag myapp:v1.0.0
buildex build . --tag myapp:v1.1.0
buildex build . --tag myapp:latest

# Workers remain running for subsequent builds
buildex status

# Clean up when done
buildex stop --all
```

## 📖 Usage

### Commands

#### `buildex build` - On-demand build

Build Docker images using on-demand EC2 buildx workers:

```bash
# Basic build
buildex build .

# Multi-platform build with tags
buildex build . --platform linux/amd64,linux/arm64 --tag myapp:latest --tag myapp:v1.0.0

# Build and push to registry
buildex build . --push --tag myregistry/myapp:latest

# Use spot instances for cost savings
buildex build . --spot --instance-type m5.large

# Build with custom Dockerfile
buildex build . --file Dockerfile.prod --target production

# Generate provenance attestation
buildex build . --provenance mode=max --push --tag myapp:latest

# Use additional build contexts
buildex build . --build-context mycontext=docker-image://alpine:latest --tag myapp:latest

# Multiple build contexts
buildex build . --build-context alpine=docker-image://alpine:latest --build-context node=docker-image://node:18 --tag myapp:latest

# Keep workers running after build
buildex build . --keep

# Use existing workers when available (automatic by default)
buildex build . --platform linux/amd64,linux/arm64

# Disable automatic existing worker detection
buildex build . --auto-use-existing=false
```

#### `buildex start` - Start workers

Start buildx workers without building:

```bash
# Start workers for all configured platforms
buildex start

# Start workers for specific platforms
buildex start --platform linux/amd64,linux/arm64

# Start with spot instances
buildex start --spot --instance-type m5.large

# Start with custom name
buildex start --name my-workers
```

#### `buildex stop` - Stop workers

Stop workers and terminate EC2 instances:

```bash
# Stop specific worker
buildex stop my-worker

# Stop all workers
buildex stop --all

# Stop workers for specific platforms
buildex stop --platform linux/arm64

# Stop but save state (don't remove from tracking)
buildex stop --all --save-state
```

#### `buildex status` - Show status

Show status of workers and builders:

```bash
# Show all workers
buildex status

# Show specific worker
buildex status my-worker

# Show detailed information
buildex status --verbose

# Show only running workers
buildex status --running

# Show builders information
buildex status --builders

# Refresh status from AWS
buildex status --refresh
```

### Configuration

Create a configuration file at `~/.buildex.yaml`:

```yaml
aws:
  region: us-east-1
  use_spot: true
  key_name: my-buildex-key

buildx:
  platforms:
    - linux/amd64
    - linux/arm64
  builder_name: my-builder

ssh:
  user: ec2-user
  port: 22
  timeout: 30s
  key_path: ""        # Auto-determined from AWS key name if empty
  strict_host: false  # Set to true for strict host key verification

timeouts:
  instance_startup: 300s
  docker_ready: 120s
  build_timeout: 3600s
```

### Environment Variables

Configure using environment variables with `BUILDEX_` prefix:

```bash
export BUILDEX_AWS_REGION=us-west-2
export BUILDEX_AWS_INSTANCE_TYPE=m5.large
export BUILDEX_AWS_USE_SPOT=true
export BUILDEX_SSH_STRICT_HOST=false
```

## 🔐 SSH Configuration

BuildEx uses SSH to communicate with EC2 instances and automatically manages SSH configuration files to ensure Docker contexts can connect properly.

### SSH Key Management

BuildEx automatically determines the SSH key path in the following order:

1. **Explicit key path**: Use `ssh.key_path` if specified
2. **Worker-specific key**: Use the key associated with each worker
3. **Auto-determined path**: Use `~/.ssh/{aws.key_name}` based on AWS configuration

**Example with auto-determined key:**
```yaml
aws:
  key_name: buildex-key  # Will use ~/.ssh/buildex-key
ssh:
  # key_path not specified - automatically determined
```

**Example with explicit key:**
```yaml
ssh:
  key_path: ~/.ssh/my-custom-key
```

### SSH Host Key Verification

**Default (Recommended for Automation):**
```yaml
ssh:
  strict_host: false  # Disables host key verification
```

**Secure Mode:**
```yaml
ssh:
  strict_host: true   # Enables host key verification
```

### Troubleshooting SSH Issues

#### Host Key Verification Failed

If you see an error like:
```
Host key verification failed
```

**Solution 1: Disable strict host checking (default)**
```yaml
ssh:
  strict_host: false
```

**Solution 2: Add host keys manually**
```bash
# For each worker IP, add the host key
ssh-keyscan -H <worker-ip> >> ~/.ssh/known_hosts

# Or let BuildEx handle it automatically with strict_host: true
```

#### Connection Timeout

Increase SSH timeout:
```yaml
ssh:
  timeout: 60s
```

#### Custom SSH User

For AMIs with different default users:
```yaml
ssh:
  user: ubuntu  # Default is ec2-user
```

#### Custom SSH Key

Use a specific SSH key:
```yaml
ssh:
  key_path: ~/.ssh/my-custom-key
```

### SSH Configuration File Management

BuildEx automatically creates SSH configuration entries in `~/.ssh/config` for each worker:

```ssh-config
# BuildEx entry for 34.245.178.178
Host 34.245.178.178
    HostName 34.245.178.178
    User ec2-user
    IdentityFile ~/.ssh/buildex-key
    IdentitiesOnly yes
    StrictHostKeyChecking no
    UserKnownHostsFile /dev/null
    ConnectTimeout 30
    ServerAliveInterval 60
    ServerAliveCountMax 3
```

This ensures Docker contexts can connect without additional SSH configuration.

## 💡 Workflow Examples

### Complete Build Workflow with Existing Workers

Here's a typical workflow that maximizes efficiency by reusing workers:

```bash
# 1. Start persistent workers for your project
buildex start --platform linux/amd64,linux/arm64 --spot --name myproject-workers

# 2. Check worker status
buildex status
# Output:
# Worker: myproject-workers-linux-amd64 (running) - 34.245.178.178
# Worker: myproject-workers-linux-arm64 (running) - 52.91.47.123

# 3. Run multiple builds using the same workers
buildex build . --tag myapp:feature-branch
# Output: 🔄 Using 2 existing worker(s), launching 0 new worker(s)

buildex build . --tag myapp:v1.0.0 --push
# Output: 🔄 Using 2 existing worker(s), launching 0 new worker(s)

# 4. Build different projects with same workers
cd ../another-project
buildex build . --tag another:latest
# Output: 🔄 Using 2 existing worker(s), launching 0 new worker(s)

# 5. Clean up when done with development session
buildex stop --all
```

### Instance Types

Choose appropriate instance types for your workload:

- **t3.medium**: Good for light builds (2 vCPU, 4 GB RAM)
- **m5.large**: Balanced compute (2 vCPU, 8 GB RAM)
- **c5.xlarge**: CPU optimized (4 vCPU, 8 GB RAM)
- **m5.2xlarge**: Heavy builds (8 vCPU, 32 GB RAM)

### Auto-termination

Workers are automatically terminated after builds unless `--keep` is specified:

```bash
# Build and auto-terminate
buildex build .

# Build and keep workers for more builds
buildex build . --keep
```

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
