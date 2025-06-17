package machineset

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

const (
	// This will be used to determine if the AMI is published on the AWS marketplace
	AWSMarketPlacePublisherID = "679593333241"
)

// getAMIMetadata examines AMI metadata and returns publisher ID, name, and any error
func getAMIMetadata(ctx context.Context, amiID string, region string, secretClient clientset.Interface) (string, string, error) {
	klog.Infof("Examining AMI metadata for %s in region %s", amiID, region)

	// Get AWS credentials from the secret
	secret, err := secretClient.CoreV1().Secrets("openshift-machine-api").Get(ctx, "aws-cloud-credentials", metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("failed to get AWS credentials secret: %w", err)
	}

	// Extract credentials from secret data
	accessKeyID, ok := secret.Data["aws_access_key_id"]
	if !ok {
		return "", "", fmt.Errorf("aws_access_key_id not found in credentials secret")
	}

	secretAccessKey, ok := secret.Data["aws_secret_access_key"]
	if !ok {
		return "", "", fmt.Errorf("aws_secret_access_key not found in credentials secret")
	}

	// Create AWS config with explicit credentials using SDK v2
	creds := credentials.NewStaticCredentialsProvider(
		string(accessKeyID),
		string(secretAccessKey),
		"", // no session token needed for static credentials
	)

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(creds),
	)
	if err != nil {
		return "", "", fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Create EC2 client
	awsClient := ec2.NewFromConfig(cfg)

	// Describe the AMI to get its metadata
	describeImagesInput := &ec2.DescribeImagesInput{
		ImageIds: []string{amiID},
	}

	result, err := awsClient.DescribeImages(ctx, describeImagesInput)
	if err != nil {
		return "", "", fmt.Errorf("failed to describe AMI %s: %w", amiID, err)
	}

	if len(result.Images) == 0 {
		return "", "", fmt.Errorf("AMI %s not found", amiID)
	}

	image := result.Images[0]

	// Extract publisher ID and name (AWS SDK v2 uses value types, not pointers)
	publisherID := aws.ToString(image.OwnerId)
	imageName := aws.ToString(image.Name)

	klog.Infof("AMI %s: Publisher %s - %s", amiID, publisherID, imageName)

	return publisherID, imageName, nil
}
