package aws

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"
)

// ensureKeyPair ensures that the specified SSH key pair exists in AWS
func (m *EC2Manager) ensureKeyPair(ctx context.Context) error {
	keyName := m.config.AWS.KeyName

	// Check if key pair exists in AWS
	_, err := m.client.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{
		KeyNames: []string{keyName},
	})

	if err == nil {
		// Key pair exists in AWS, check if local private key exists
		keyPath, err := GetSSHKeyPath(keyName)
		if err != nil {
			return fmt.Errorf("failed to get SSH key path: %w", err)
		}

		if _, err := os.Stat(keyPath); err == nil {
			logrus.WithField("key_name", keyName).Debug("SSH key pair already exists")
			return nil
		}

		// Key exists in AWS but not locally - this is a problem
		return fmt.Errorf("key pair '%s' exists in AWS but private key not found locally at %s", keyName, keyPath)
	}

	// Key pair doesn't exist, create it
	logrus.WithField("key_name", keyName).Info("Creating new SSH key pair")

	return m.createKeyPair(ctx, keyName)
}

// createKeyPair creates a new SSH key pair
func (m *EC2Manager) createKeyPair(ctx context.Context, keyName string) error {
	// Generate RSA private key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	// Generate public key
	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return fmt.Errorf("failed to generate public key: %w", err)
	}

	// Import key pair to AWS
	_, err = m.client.ImportKeyPair(ctx, &ec2.ImportKeyPairInput{
		KeyName:           aws.String(keyName),
		PublicKeyMaterial: ssh.MarshalAuthorizedKey(publicKey),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeKeyPair,
				Tags: []types.Tag{
					{Key: aws.String("Name"), Value: aws.String(keyName)},
					{Key: aws.String("ManagedBy"), Value: aws.String("buildex")},
					{Key: aws.String("CreatedAt"), Value: aws.String(fmt.Sprintf("%d", ctx.Value("timestamp")))},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to import key pair to AWS: %w", err)
	}

	// Save private key locally
	if err := savePrivateKey(privateKey, keyName); err != nil {
		// Try to delete the key pair from AWS since we couldn't save locally
		m.client.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{
			KeyName: aws.String(keyName),
		})
		return fmt.Errorf("failed to save private key locally: %w", err)
	}

	logrus.WithField("key_name", keyName).Info("SSH key pair created successfully")
	return nil
}

// savePrivateKey saves the private key to the local filesystem
func savePrivateKey(privateKey *rsa.PrivateKey, keyName string) error {
	// Get the SSH directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	sshDir := filepath.Join(homeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("failed to create .ssh directory: %w", err)
	}

	// Encode private key to PEM format
	privateKeyPEM := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}

	// Write private key to file
	keyPath := filepath.Join(sshDir, keyName)
	keyFile, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create private key file: %w", err)
	}
	defer keyFile.Close()

	if err := pem.Encode(keyFile, privateKeyPEM); err != nil {
		return fmt.Errorf("failed to encode private key: %w", err)
	}

	logrus.WithField("key_path", keyPath).Debug("Private key saved locally")
	return nil
}

// GetSSHKeyPath returns the path to the SSH private key
func GetSSHKeyPath(keyName string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	return filepath.Join(homeDir, ".ssh", keyName), nil
}

// DeleteKeyPair deletes a key pair from AWS and optionally from local filesystem
func (m *EC2Manager) DeleteKeyPair(ctx context.Context, keyName string, deleteLocal bool) error {
	logrus.WithField("key_name", keyName).Info("Deleting SSH key pair")

	// Delete from AWS
	_, err := m.client.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{
		KeyName: aws.String(keyName),
	})
	if err != nil {
		return fmt.Errorf("failed to delete key pair from AWS: %w", err)
	}

	// Delete local private key if requested
	if deleteLocal {
		keyPath, err := GetSSHKeyPath(keyName)
		if err != nil {
			logrus.WithError(err).Warn("Failed to get SSH key path for deletion")
			return nil // Don't fail the operation for this
		}

		if err := os.Remove(keyPath); err != nil && !os.IsNotExist(err) {
			logrus.WithError(err).WithField("key_path", keyPath).Warn("Failed to delete local private key")
		} else {
			logrus.WithField("key_path", keyPath).Debug("Local private key deleted")
		}
	}

	logrus.WithField("key_name", keyName).Info("SSH key pair deleted successfully")
	return nil
}

// ListKeyPairs lists all buildex-managed key pairs
func (m *EC2Manager) ListKeyPairs(ctx context.Context) ([]types.KeyPairInfo, error) {
	result, err := m.client.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("tag:ManagedBy"),
				Values: []string{"buildex"},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe key pairs: %w", err)
	}

	return result.KeyPairs, nil
}

// ValidateKeyPair validates that a key pair exists and is accessible
func (m *EC2Manager) ValidateKeyPair(ctx context.Context, keyName string) error {
	// Check AWS
	_, err := m.client.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{
		KeyNames: []string{keyName},
	})
	if err != nil {
		return fmt.Errorf("key pair '%s' not found in AWS: %w", keyName, err)
	}

	// Check local file
	keyPath, err := GetSSHKeyPath(keyName)
	if err != nil {
		return fmt.Errorf("failed to get SSH key path: %w", err)
	}

	if _, err := os.Stat(keyPath); err != nil {
		return fmt.Errorf("private key file not found at %s: %w", keyPath, err)
	}

	// Try to parse the private key
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("failed to read private key file: %w", err)
	}

	_, err = ssh.ParsePrivateKey(keyData)
	if err != nil {
		return fmt.Errorf("failed to parse private key: %w", err)
	}

	return nil
}
