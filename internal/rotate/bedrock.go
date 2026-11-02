package rotate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"

	"github.com/credorbit/credorbit/internal/model"
)

// Bedrock verification error classes; test with errors.Is.
var (
	ErrBedrockAccessDenied  = errors.New("no Bedrock access in this account/region")
	ErrModelNotAvailable    = errors.New("model not available")
	ErrBedrockNotFound      = errors.New("bedrock resource not found")
	ErrBedrockThrottled     = errors.New("bedrock throttled")
	ErrBedrockInvalidCreds  = errors.New("credentials rejected by AWS")
	ErrBedrockModelRequired = errors.New("bedrock model ID is required")
)

// BedrockVerifier sends a one-token Converse request to prove that an
// account can invoke a model. BaseEndpoint overrides the regional endpoint
// (tests, VPC endpoints); empty uses the standard AWS endpoint.
type BedrockVerifier struct {
	HTTP         *http.Client
	BaseEndpoint string
}

// VerifyBedrock checks Bedrock access with the given credentials using the
// standard regional endpoint.
func VerifyBedrock(ctx context.Context, hc *http.Client, c model.Credentials, region, modelID string) error {
	return BedrockVerifier{HTTP: hc}.Verify(ctx, c, region, modelID)
}

// Verify returns nil when a minimal Converse call succeeds.
func (v BedrockVerifier) Verify(ctx context.Context, c model.Credentials, region, modelID string) error {
	if strings.TrimSpace(modelID) == "" {
		return ErrBedrockModelRequired
	}
	if strings.TrimSpace(region) == "" {
		return errors.New("bedrock region is required")
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return fmt.Errorf("%w: credentials are incomplete", ErrBedrockInvalidCreds)
	}
	creds := c // copied so the provider never sees later mutation
	cfg := aws.Config{
		Region: region,
		Credentials: aws.NewCredentialsCache(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{
				AccessKeyID:     creds.AccessKeyID,
				SecretAccessKey: creds.SecretAccessKey,
				SessionToken:    creds.SessionToken,
				Source:          "credorbit",
				CanExpire:       !creds.Expiration.IsZero(),
				Expires:         creds.Expiration,
			}, nil
		})),
		RetryMaxAttempts: 1, // a probe: report throttling instead of waiting it out
	}
	if v.HTTP != nil {
		cfg.HTTPClient = v.HTTP
	}
	client := bedrockruntime.NewFromConfig(cfg, func(o *bedrockruntime.Options) {
		if v.BaseEndpoint != "" {
			o.BaseEndpoint = aws.String(v.BaseEndpoint)
		}
	})
	_, err := client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: aws.String(modelID),
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "ping"}},
		}},
		InferenceConfig: &types.InferenceConfiguration{MaxTokens: aws.Int32(1)},
	})
	if err == nil {
		return nil
	}
	return classifyBedrock(err, region, modelID)
}

func classifyBedrock(err error, region, modelID string) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("bedrock check: %w", model.ErrCanceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("bedrock check timed out: %w", model.ErrTransient)
	}
	var api smithy.APIError
	if !errors.As(err, &api) {
		return fmt.Errorf("bedrock check in %s: %w: %v", region, model.ErrTransient, err)
	}
	msg := api.ErrorMessage()
	switch api.ErrorCode() {
	case "AccessDeniedException":
		return fmt.Errorf("%w (%s): %s", ErrBedrockAccessDenied, region, msg)
	case "ValidationException":
		l := strings.ToLower(msg)
		if strings.Contains(l, "model") || strings.Contains(l, "inference profile") || strings.Contains(l, "throughput") {
			return fmt.Errorf("%w: %s in %s: %s", ErrModelNotAvailable, modelID, region, msg)
		}
		return fmt.Errorf("bedrock validation error: %s", msg)
	case "ResourceNotFoundException":
		return fmt.Errorf("%w: %s in %s: %s", ErrBedrockNotFound, modelID, region, msg)
	case "ModelNotReadyException":
		return fmt.Errorf("%w: %s not ready: %s", ErrModelNotAvailable, modelID, msg)
	case "ThrottlingException", "ServiceQuotaExceededException", "TooManyRequestsException":
		return fmt.Errorf("%w: %w: %s", ErrBedrockThrottled, model.ErrTransient, msg)
	case "UnrecognizedClientException", "InvalidSignatureException", "ExpiredTokenException", "InvalidClientTokenId":
		return fmt.Errorf("%w: %s", ErrBedrockInvalidCreds, msg)
	case "ServiceUnavailableException", "InternalServerException", "ModelTimeoutException":
		return fmt.Errorf("bedrock check: %w: %s", model.ErrTransient, msg)
	}
	return fmt.Errorf("bedrock check failed (%s): %s", api.ErrorCode(), msg)
}
