package aws

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/sirupsen/logrus"
	"github.com/x-qdo/buildex/internal/config"
)

// EC2Manager manages EC2 instances for buildx workers
type EC2Manager struct {
	client *ec2.Client
	config *config.Config
	region string
}

// InstanceInfo contains information about an EC2 instance
type InstanceInfo struct {
	InstanceID   string
	PublicIP     string
	PrivateIP    string
	State        string
	InstanceType string
	Platform     string
	IsSpot       bool
	LaunchTime   time.Time
	Tags         map[string]string
}

// NewEC2Manager creates a new EC2Manager
func NewEC2Manager(cfg *config.Config) (*EC2Manager, error) {
	awsConfig, err := awsconfig.LoadDefaultConfig(context.TODO(),
		awsconfig.WithRegion(cfg.AWS.Region),
		awsconfig.WithSharedConfigProfile(cfg.AWS.Profile),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &EC2Manager{
		client: ec2.NewFromConfig(awsConfig),
		config: cfg,
		region: cfg.AWS.Region,
	}, nil
}

// LaunchInstance launches a new EC2 instance
func (m *EC2Manager) LaunchInstance(ctx context.Context, name, platform string, useSpot bool) (*InstanceInfo, error) {
	logrus.WithFields(logrus.Fields{
		"name":     name,
		"platform": platform,
		"spot":     useSpot,
	}).Info("Launching EC2 instance")

	// Get AMI ID for the platform
	amiID, err := m.getAMI(ctx, platform)
	if err != nil {
		return nil, fmt.Errorf("failed to get AMI: %w", err)
	}

	// Ensure security group exists
	sgID, err := m.ensureSecurityGroup(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure security group: %w", err)
	}

	// Ensure key pair exists
	if err := m.ensureKeyPair(ctx); err != nil {
		return nil, fmt.Errorf("failed to ensure key pair: %w", err)
	}

	// Create launch template
	userData := m.generateUserData()

	// Get platform-specific configuration
	instanceType := m.config.GetInstanceTypeForPlatform(platform)
	volumeSize := m.config.GetVolumeSizeForPlatform(platform)

	runInstancesInput := &ec2.RunInstancesInput{
		ImageId:          aws.String(amiID),
		InstanceType:     types.InstanceType(instanceType),
		MinCount:         aws.Int32(1),
		MaxCount:         aws.Int32(1),
		KeyName:          aws.String(m.config.AWS.KeyName),
		SecurityGroupIds: []string{sgID},
		UserData:         aws.String(userData),
		BlockDeviceMappings: []types.BlockDeviceMapping{
			{
				DeviceName: aws.String("/dev/xvda"),
				Ebs: &types.EbsBlockDevice{
					VolumeSize: aws.Int32(int32(volumeSize)),
					VolumeType: types.VolumeTypeGp3,
				},
			},
		},
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeInstance,
				Tags:         m.createTags(name, platform),
			},
		},
	}

	// Add subnet if specified
	if m.config.AWS.SubnetID != "" {
		runInstancesInput.SubnetId = aws.String(m.config.AWS.SubnetID)
	}

	var instanceID string

	if useSpot {
		instanceID, err = m.launchSpotInstance(ctx, runInstancesInput, platform)
	} else {
		instanceID, err = m.launchRegularInstance(ctx, runInstancesInput)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to launch instance: %w", err)
	}

	// Wait for instance to be running
	if err := m.waitForInstanceRunning(ctx, instanceID); err != nil {
		return nil, fmt.Errorf("instance failed to start: %w", err)
	}

	// Get instance info
	info, err := m.GetInstanceInfo(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get instance info: %w", err)
	}

	logrus.WithFields(logrus.Fields{
		"instance_id": instanceID,
		"public_ip":   info.PublicIP,
		"private_ip":  info.PrivateIP,
	}).Info("Instance launched successfully")

	return info, nil
}

// launchRegularInstance launches a regular (on-demand) instance
func (m *EC2Manager) launchRegularInstance(ctx context.Context, input *ec2.RunInstancesInput) (string, error) {
	result, err := m.client.RunInstances(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to run instance: %w", err)
	}

	if len(result.Instances) == 0 {
		return "", fmt.Errorf("no instances returned from RunInstances")
	}

	return *result.Instances[0].InstanceId, nil
}

// launchSpotInstance launches a spot instance
func (m *EC2Manager) launchSpotInstance(ctx context.Context, input *ec2.RunInstancesInput, platform string) (string, error) {
	// Convert BlockDeviceMappings for spot instance
	var spotBlockDevices []types.BlockDeviceMapping
	for _, bdm := range input.BlockDeviceMappings {
		spotBlockDevices = append(spotBlockDevices, bdm)
	}

	spotRequest := &ec2.RequestSpotInstancesInput{
		InstanceCount: aws.Int32(1),
		LaunchSpecification: &types.RequestSpotLaunchSpecification{
			ImageId:             input.ImageId,
			InstanceType:        input.InstanceType,
			KeyName:             input.KeyName,
			SecurityGroupIds:    input.SecurityGroupIds,
			UserData:            input.UserData,
			BlockDeviceMappings: spotBlockDevices,
		},
	}

	if input.SubnetId != nil {
		spotRequest.LaunchSpecification.SubnetId = input.SubnetId
	}

	result, err := m.client.RequestSpotInstances(ctx, spotRequest)
	if err != nil {
		return "", fmt.Errorf("failed to request spot instance: %w", err)
	}

	if len(result.SpotInstanceRequests) == 0 {
		return "", fmt.Errorf("no spot instance requests returned")
	}

	spotRequestID := *result.SpotInstanceRequests[0].SpotInstanceRequestId

	// Wait for spot request to be fulfilled
	instanceID, err := m.waitForSpotRequestFulfilled(ctx, spotRequestID)
	if err != nil {
		return "", fmt.Errorf("spot request failed: %w", err)
	}

	// Tag the instance
	if len(input.TagSpecifications) > 0 && len(input.TagSpecifications[0].Tags) > 0 {
		_, err = m.client.CreateTags(ctx, &ec2.CreateTagsInput{
			Resources: []string{instanceID},
			Tags:      input.TagSpecifications[0].Tags,
		})
		if err != nil {
			logrus.WithError(err).Warn("Failed to tag spot instance")
		}
	}

	return instanceID, nil
}

// TerminateInstance terminates an EC2 instance
func (m *EC2Manager) TerminateInstance(ctx context.Context, instanceID string) error {
	logrus.WithField("instance_id", instanceID).Info("Terminating instance")

	_, err := m.client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return fmt.Errorf("failed to terminate instance: %w", err)
	}

	return nil
}

// GetInstanceInfo retrieves information about an instance
func (m *EC2Manager) GetInstanceInfo(ctx context.Context, instanceID string) (*InstanceInfo, error) {
	result, err := m.client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe instance: %w", err)
	}

	if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
		return nil, fmt.Errorf("instance not found")
	}

	instance := result.Reservations[0].Instances[0]

	info := &InstanceInfo{
		InstanceID:   *instance.InstanceId,
		State:        string(instance.State.Name),
		InstanceType: string(instance.InstanceType),
		LaunchTime:   *instance.LaunchTime,
		Tags:         make(map[string]string),
	}

	if instance.PublicIpAddress != nil {
		info.PublicIP = *instance.PublicIpAddress
	}

	if instance.PrivateIpAddress != nil {
		info.PrivateIP = *instance.PrivateIpAddress
	}

	// Extract platform from tags
	for _, tag := range instance.Tags {
		if tag.Key != nil && tag.Value != nil {
			info.Tags[*tag.Key] = *tag.Value
			if *tag.Key == "Platform" {
				info.Platform = *tag.Value
			}
		}
	}

	// Check if it's a spot instance
	info.IsSpot = instance.SpotInstanceRequestId != nil

	return info, nil
}

// ListInstances lists all buildex instances
func (m *EC2Manager) ListInstances(ctx context.Context) ([]*InstanceInfo, error) {
	result, err := m.client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("tag:ManagedBy"),
				Values: []string{"buildex"},
			},
			{
				Name:   aws.String("instance-state-name"),
				Values: []string{"pending", "running", "stopping", "stopped"},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe instances: %w", err)
	}

	var instances []*InstanceInfo
	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			info := &InstanceInfo{
				InstanceID:   *instance.InstanceId,
				State:        string(instance.State.Name),
				InstanceType: string(instance.InstanceType),
				LaunchTime:   *instance.LaunchTime,
				Tags:         make(map[string]string),
			}

			if instance.PublicIpAddress != nil {
				info.PublicIP = *instance.PublicIpAddress
			}

			if instance.PrivateIpAddress != nil {
				info.PrivateIP = *instance.PrivateIpAddress
			}

			for _, tag := range instance.Tags {
				if tag.Key != nil && tag.Value != nil {
					info.Tags[*tag.Key] = *tag.Value
					if *tag.Key == "Platform" {
						info.Platform = *tag.Value
					}
				}
			}

			info.IsSpot = instance.SpotInstanceRequestId != nil
			instances = append(instances, info)
		}
	}

	return instances, nil
}

// waitForInstanceRunning waits for an instance to be in running state
func (m *EC2Manager) waitForInstanceRunning(ctx context.Context, instanceID string) error {
	logrus.WithField("instance_id", instanceID).Info("Waiting for instance to be running")

	waiter := ec2.NewInstanceRunningWaiter(m.client)
	return waiter.Wait(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}, m.config.Timeouts.InstanceStartup)
}

// waitForSpotRequestFulfilled waits for a spot request to be fulfilled
func (m *EC2Manager) waitForSpotRequestFulfilled(ctx context.Context, spotRequestID string) (string, error) {
	logrus.WithField("spot_request_id", spotRequestID).Info("Waiting for spot request to be fulfilled")

	timeout := time.After(m.config.Timeouts.InstanceStartup)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return "", fmt.Errorf("timeout waiting for spot request to be fulfilled")
		case <-ticker.C:
			result, err := m.client.DescribeSpotInstanceRequests(ctx, &ec2.DescribeSpotInstanceRequestsInput{
				SpotInstanceRequestIds: []string{spotRequestID},
			})
			if err != nil {
				return "", fmt.Errorf("failed to describe spot request: %w", err)
			}

			if len(result.SpotInstanceRequests) == 0 {
				return "", fmt.Errorf("spot request not found")
			}

			request := result.SpotInstanceRequests[0]
			state := string(request.State)

			logrus.WithFields(logrus.Fields{
				"state":  state,
				"status": request.Status,
			}).Debug("Spot request status")

			switch state {
			case "active":
				if request.InstanceId != nil {
					return *request.InstanceId, nil
				}
			case "failed", "cancelled", "closed":
				return "", fmt.Errorf("spot request failed with state: %s", state)
			}
		}
	}
}

// getAMI returns the appropriate AMI ID for the given platform
func (m *EC2Manager) getAMI(ctx context.Context, platform string) (string, error) {
	// If AMI is specified in config, use it
	if m.config.AWS.AMI != "" {
		return m.config.AWS.AMI, nil
	}

	// Otherwise, find the latest Amazon Linux 2023 AMI
	var architecture string
	if strings.Contains(platform, "arm64") || strings.Contains(platform, "aarch64") {
		architecture = "arm64"
	} else {
		architecture = "x86_64"
	}

	result, err := m.client.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{"amazon"},
		Filters: []types.Filter{
			{
				Name:   aws.String("name"),
				Values: []string{"al2023-ami-2023*"},
			},
			{
				Name:   aws.String("architecture"),
				Values: []string{architecture},
			},
			{
				Name:   aws.String("state"),
				Values: []string{"available"},
			},
			{
				Name:   aws.String("root-device-type"),
				Values: []string{"ebs"},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to describe images: %w", err)
	}

	if len(result.Images) == 0 {
		return "", fmt.Errorf("no suitable AMI found for architecture %s", architecture)
	}

	// Find the most recent image
	var latestImage types.Image
	for _, image := range result.Images {
		if latestImage.CreationDate == nil || *image.CreationDate > *latestImage.CreationDate {
			latestImage = image
		}
	}

	return *latestImage.ImageId, nil
}

// createTags creates tags for the instance
func (m *EC2Manager) createTags(name, platform string) []types.Tag {
	tags := []types.Tag{
		{Key: aws.String("Name"), Value: aws.String(name)},
		{Key: aws.String("ManagedBy"), Value: aws.String("buildex")},
		{Key: aws.String("Platform"), Value: aws.String(platform)},
		{Key: aws.String("CreatedAt"), Value: aws.String(time.Now().Format(time.RFC3339))},
	}

	// Add custom tags from config
	for key, value := range m.config.AWS.Tags {
		tags = append(tags, types.Tag{
			Key:   aws.String(key),
			Value: aws.String(value),
		})
	}

	return tags
}

// generateUserData generates user data script for instance initialization
func (m *EC2Manager) generateUserData() string {
	script := `#!/bin/bash
# Amazon Linux 2023 initialization script for BuildEx

# Update system packages
dnf update -y

# Install Docker and additional tools
dnf install -y --allowerasing docker git curl

# Start and enable Docker service
systemctl start docker
systemctl enable docker

# Add ec2-user to docker group
usermod -a -G docker ec2-user

# Ensure docker group has proper permissions
chmod 666 /var/run/docker.sock

# Configure Docker daemon with BuildKit and optimizations
mkdir -p /etc/docker
cat > /etc/docker/daemon.json << 'EOF'
{
  "features": {
    "buildkit": true
  },
  "experimental": false,
  "log-driver": "json-file",
  "log-opts": {
    "max-size": "10m",
    "max-file": "3"
  }
}
EOF

# Restart Docker to apply configuration
systemctl restart docker

# Wait for Docker to be ready
echo "Waiting for Docker to be ready..."
until docker info > /dev/null 2>&1; do
    sleep 2
done

# Create buildex user directory structure
mkdir -p /home/ec2-user/.buildex
chown ec2-user:ec2-user /home/ec2-user/.buildex

# Set up Docker context for buildx
sudo -u ec2-user docker context create buildex-remote || true

# Create a marker file to indicate setup is complete
touch /tmp/buildex-ready
echo "BuildEx initialization completed at $(date)" > /tmp/buildex-ready

# Log completion
echo "$(date): BuildEx EC2 instance initialization completed" >> /var/log/buildex-init.log
`
	return base64.StdEncoding.EncodeToString([]byte(script))
}

// ensureSecurityGroup ensures that the security group exists and has the correct rules
func (m *EC2Manager) ensureSecurityGroup(ctx context.Context) (string, error) {
	sgName := m.config.AWS.SecurityGroup
	if sgName == "" {
		sgName = "buildex-sg"
	}

	// Check if security group exists
	result, err := m.client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("group-name"),
				Values: []string{sgName},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to describe security groups: %w", err)
	}

	var sgID string
	if len(result.SecurityGroups) > 0 {
		sgID = *result.SecurityGroups[0].GroupId
		logrus.WithFields(logrus.Fields{
			"sg_name": sgName,
			"sg_id":   sgID,
		}).Debug("Security group already exists")
	} else {
		// Create security group
		sgID, err = m.createSecurityGroup(ctx, sgName)
		if err != nil {
			return "", fmt.Errorf("failed to create security group: %w", err)
		}
	}

	// Ensure rules are correct
	if err := m.ensureSecurityGroupRules(ctx, sgID); err != nil {
		return "", fmt.Errorf("failed to ensure security group rules: %w", err)
	}

	return sgID, nil
}

// createSecurityGroup creates a new security group
func (m *EC2Manager) createSecurityGroup(ctx context.Context, sgName string) (string, error) {
	logrus.WithField("sg_name", sgName).Info("Creating security group")

	result, err := m.client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(sgName),
		Description: aws.String("BuildEx security group for Docker buildx workers"),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeSecurityGroup,
				Tags: []types.Tag{
					{Key: aws.String("Name"), Value: aws.String(sgName)},
					{Key: aws.String("ManagedBy"), Value: aws.String("buildex")},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create security group: %w", err)
	}

	sgID := *result.GroupId
	logrus.WithFields(logrus.Fields{
		"sg_name": sgName,
		"sg_id":   sgID,
	}).Info("Security group created")

	return sgID, nil
}

// ensureSecurityGroupRules ensures the security group has the correct ingress and egress rules
func (m *EC2Manager) ensureSecurityGroupRules(ctx context.Context, sgID string) error {
	// Get current rules
	result, err := m.client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		GroupIds: []string{sgID},
	})
	if err != nil {
		return fmt.Errorf("failed to describe security group: %w", err)
	}

	if len(result.SecurityGroups) == 0 {
		return fmt.Errorf("security group not found")
	}

	sg := result.SecurityGroups[0]

	// Track existing ingress rules
	existingIngressRules := make(map[string]bool)
	for _, rule := range sg.IpPermissions {
		if rule.IpProtocol != nil && rule.FromPort != nil {
			key := fmt.Sprintf("%s:%d", *rule.IpProtocol, *rule.FromPort)
			existingIngressRules[key] = true
		}
	}

	// Track existing egress rules
	existingEgressRules := make(map[string]bool)
	for _, rule := range sg.IpPermissionsEgress {
		if rule.IpProtocol != nil {
			if rule.FromPort != nil {
				key := fmt.Sprintf("%s:%d", *rule.IpProtocol, *rule.FromPort)
				existingEgressRules[key] = true
			} else {
				// For rules like "all traffic" (-1 protocol)
				key := fmt.Sprintf("%s:all", *rule.IpProtocol)
				existingEgressRules[key] = true
			}
		}
	}

	// Required ingress rules for buildx workers
	requiredIngressRules := []types.IpPermission{
		{
			IpProtocol: aws.String("tcp"),
			FromPort:   aws.Int32(22),
			ToPort:     aws.Int32(22),
			IpRanges: []types.IpRange{
				{CidrIp: aws.String("0.0.0.0/0"), Description: aws.String("SSH access")},
			},
		},
		{
			IpProtocol: aws.String("tcp"),
			FromPort:   aws.Int32(2376),
			ToPort:     aws.Int32(2376),
			IpRanges: []types.IpRange{
				{CidrIp: aws.String("0.0.0.0/0"), Description: aws.String("Docker daemon TLS")},
			},
		},
	}

	// Required egress rules for buildx workers
	requiredEgressRules := []types.IpPermission{
		{
			IpProtocol: aws.String("-1"), // All protocols
			IpRanges: []types.IpRange{
				{CidrIp: aws.String("0.0.0.0/0"), Description: aws.String("Allow all outbound traffic")},
			},
		},
	}

	// Add missing ingress rules
	var ingressRulesToAdd []types.IpPermission
	for _, rule := range requiredIngressRules {
		key := fmt.Sprintf("%s:%d", *rule.IpProtocol, *rule.FromPort)
		if !existingIngressRules[key] {
			ingressRulesToAdd = append(ingressRulesToAdd, rule)
		}
	}

	if len(ingressRulesToAdd) > 0 {
		logrus.WithField("sg_id", sgID).Info("Adding missing security group ingress rules")

		_, err = m.client.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
			GroupId:       aws.String(sgID),
			IpPermissions: ingressRulesToAdd,
		})
		if err != nil {
			return fmt.Errorf("failed to authorize security group ingress: %w", err)
		}
	}

	// Add missing egress rules
	var egressRulesToAdd []types.IpPermission
	for _, rule := range requiredEgressRules {
		key := fmt.Sprintf("%s:all", *rule.IpProtocol)
		if !existingEgressRules[key] {
			egressRulesToAdd = append(egressRulesToAdd, rule)
		}
	}

	if len(egressRulesToAdd) > 0 {
		logrus.WithField("sg_id", sgID).Info("Adding missing security group egress rules")

		_, err = m.client.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{
			GroupId:       aws.String(sgID),
			IpPermissions: egressRulesToAdd,
		})
		if err != nil {
			return fmt.Errorf("failed to authorize security group egress: %w", err)
		}
	}

	return nil
}
