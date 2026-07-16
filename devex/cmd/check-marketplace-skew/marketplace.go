package main

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	bootimagemarketplace "github.com/openshift/machine-config-operator/pkg/controller/bootimage/marketplace"
)

// awsMarketplaceOwnerAlias is the owner alias used in DescribeImages filters for marketplace AMIs.
const awsMarketplaceOwnerAlias = "aws-marketplace"

// NewEC2Client builds an EC2 client from environment-provided AWS credentials. STS web-identity
// (AWS_ROLE_ARN/AWS_WEB_IDENTITY_TOKEN_FILE, the mechanism CI OIDC federation uses) is tried
// first, falling back to static keys (AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY[/AWS_SESSION_TOKEN]).
// This mirrors pkg/controller/bootimage/aws_helpers.go's newAWSEC2Client, so this standalone CLI
// only needs the AWS SDK packages already vendored for the controller — no
// config.LoadDefaultConfig (and its SSO/SSOOIDC/IMDS dependency chain) required.
func NewEC2Client(_ context.Context, region string) (*ec2.Client, error) {
	roleARN := os.Getenv("AWS_ROLE_ARN")
	tokenFile := os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE")
	if roleARN != "" && tokenFile != "" {
		stsClient := sts.New(sts.Options{Region: region})
		creds := aws.NewCredentialsCache(stscreds.NewWebIdentityRoleProvider(
			stsClient, roleARN, stscreds.IdentityTokenFile(tokenFile),
			func(o *stscreds.WebIdentityRoleOptions) {
				o.RoleSessionName = "check-marketplace-skew"
			},
		))
		return ec2.New(ec2.Options{Region: region, Credentials: creds}), nil
	}

	accessKeyID := os.Getenv("AWS_ACCESS_KEY_ID")
	secretAccessKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	if accessKeyID != "" && secretAccessKey != "" {
		return ec2.New(ec2.Options{
			Region:      region,
			Credentials: credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, os.Getenv("AWS_SESSION_TOKEN")),
		}), nil
	}

	return nil, fmt.Errorf("no AWS credentials found: set AWS_ROLE_ARN + AWS_WEB_IDENTITY_TOKEN_FILE, or AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY")
}

// DescribeMarketplaceAMIs returns every published Marketplace AMI whose name contains productID.
func DescribeMarketplaceAMIs(ctx context.Context, client *ec2.Client, productID string) ([]ec2types.Image, error) {
	out, err := client.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{awsMarketplaceOwnerAlias},
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("name"),
				Values: []string{"*" + productID + "*"},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("DescribeImages failed for product ID %s: %w", productID, err)
	}
	return out.Images, nil
}

// tokenInBand reports whether token falls within [floor, ceiling], inclusive.
func tokenInBand(token, floor, ceiling string) bool {
	return bootimagemarketplace.CmpVersionToken(token, floor) >= 0 && bootimagemarketplace.CmpVersionToken(token, ceiling) <= 0
}

// AMIMatch describes the Marketplace AMI, if any, that satisfied a product's band check.
type AMIMatch struct {
	ImageID, Name, Description, Version, Token string
}

// ProductResult is the pass/fail outcome of the band check for a single Marketplace product.
type ProductResult struct {
	ProductID, ProductName string
	Pass                   bool
	MatchedAMI             *AMIMatch
	CandidateCount         int    // how many AMIs were found in total, for FAIL diagnostics
	Reason                 string // set on FAIL or error
}

// CheckProduct is existential, not singular: it enumerates every published Marketplace AMI for
// product and passes if at least one falls within [floor, ceiling]. Marketplace may keep multiple
// AMI versions live at once, so the default/latest one being out of band doesn't mean customers
// have no compliant option.
func CheckProduct(ctx context.Context, client *ec2.Client, product ProductSpec, floor, ceiling string) (ProductResult, error) {
	result := ProductResult{ProductID: product.ID, ProductName: product.Name}

	images, err := DescribeMarketplaceAMIs(ctx, client, product.ID)
	if err != nil {
		return ProductResult{}, err
	}
	result.CandidateCount = len(images)

	var best *AMIMatch
	for _, img := range images {
		description := aws.ToString(img.Description)
		fullVersion, token, ok := bootimagemarketplace.ExtractVersionFromDescription(description)
		if !ok {
			continue
		}
		if !tokenInBand(token, floor, ceiling) {
			continue
		}
		if best == nil || bootimagemarketplace.CmpVersionToken(token, best.Token) > 0 {
			best = &AMIMatch{
				ImageID:     aws.ToString(img.ImageId),
				Name:        aws.ToString(img.Name),
				Description: description,
				Version:     fullVersion,
				Token:       token,
			}
		}
	}

	if best == nil {
		result.Pass = false
		result.Reason = fmt.Sprintf("no published AMI in band [%s, %s] out of %d candidate(s)", floor, ceiling, result.CandidateCount)
		return result, nil
	}

	result.Pass = true
	result.MatchedAMI = best
	return result, nil
}
